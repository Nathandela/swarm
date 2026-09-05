package pushgw

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	firestorepb "cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type retryFirestore struct {
	firestorepb.UnimplementedFirestoreServer
	attempt int
}

type claimProbeFirestore struct {
	firestorepb.UnimplementedFirestoreServer
	readTime    time.Time
	readTimes   []time.Time
	commitTime  time.Time
	abortFirst  bool
	attempt     int
	documents   []string
	writeCounts []int
	wakeRecords map[int]*wakeAttemptRecord
}

type healthProbeFirestore struct {
	firestorepb.UnimplementedFirestoreServer
	request *firestorepb.RunQueryRequest
	err     error
}

func (s *healthProbeFirestore) RunQuery(request *firestorepb.RunQueryRequest, _ firestorepb.Firestore_RunQueryServer) error {
	s.request = request
	return s.err
}

func TestFirestoreHealthCheckUsesBoundedQueryAndPropagatesMissingDatabase(t *testing.T) {
	probe := &healthProbeFirestore{}
	client := newFirestoreTestClient(t, probe)
	repo := newFirestorePersistence(client, "health")
	if err := repo.healthCheck(context.Background()); err != nil {
		t.Fatalf("empty collection health check: %v", err)
	}
	if probe.request == nil || probe.request.GetStructuredQuery().GetLimit().GetValue() != 1 {
		t.Fatalf("health query was not bounded to one document: %+v", probe.request)
	}

	probe.err = status.Error(codes.NotFound, "database does not exist")
	if err := repo.healthCheck(context.Background()); status.Code(err) != codes.NotFound {
		t.Fatalf("missing database error = %v, want NotFound preserved", err)
	}
}

func (s *claimProbeFirestore) BeginTransaction(context.Context, *firestorepb.BeginTransactionRequest) (*firestorepb.BeginTransactionResponse, error) {
	s.attempt++
	return &firestorepb.BeginTransactionResponse{Transaction: []byte{byte(s.attempt)}}, nil
}

func (s *claimProbeFirestore) timeFor(transaction []byte) time.Time {
	if len(transaction) > 0 && int(transaction[0]) <= len(s.readTimes) {
		return s.readTimes[int(transaction[0])-1]
	}
	return s.readTime
}

func (s *claimProbeFirestore) BatchGetDocuments(request *firestorepb.BatchGetDocumentsRequest, stream firestorepb.Firestore_BatchGetDocumentsServer) error {
	readTime := s.timeFor(request.GetTransaction())
	attempt := 0
	if len(request.GetTransaction()) > 0 {
		attempt = int(request.GetTransaction()[0])
	}
	for _, name := range request.Documents {
		s.documents = append(s.documents, name)
		response := &firestorepb.BatchGetDocumentsResponse{ReadTime: timestamppb.New(readTime)}
		switch {
		case strings.Contains(name, "_addresses/"):
			response.Result = &firestorepb.BatchGetDocumentsResponse_Found{Found: &firestorepb.Document{
				Name: name,
				Fields: map[string]*firestorepb.Value{
					"installation_id": {ValueType: &firestorepb.Value_StringValue{StringValue: "installation"}},
					"submit_cap_hash": {ValueType: &firestorepb.Value_StringValue{StringValue: hashSecret("cap")}},
					"bound":           {ValueType: &firestorepb.Value_BooleanValue{BooleanValue: true}},
				},
				CreateTime: timestamppb.New(readTime),
				UpdateTime: timestamppb.New(readTime),
			}}
		case strings.Contains(name, "_installations/"):
			response.Result = &firestorepb.BatchGetDocumentsResponse_Found{Found: &firestorepb.Document{
				Name: name,
				Fields: map[string]*firestorepb.Value{
					"fcm_token_enc":     {ValueType: &firestorepb.Value_BytesValue{BytesValue: []byte("token")}},
					"token_key_version": {ValueType: &firestorepb.Value_StringValue{StringValue: "v1"}},
					"token_generation":  {ValueType: &firestorepb.Value_IntegerValue{IntegerValue: 1}},
					"last_active_ms":    {ValueType: &firestorepb.Value_IntegerValue{IntegerValue: readTime.UnixMilli()}},
				},
				CreateTime: timestamppb.New(readTime),
				UpdateTime: timestamppb.New(readTime),
			}}
		case strings.Contains(name, "_wake_attempts/") && s.wakeRecords[attempt] != nil:
			record := s.wakeRecords[attempt]
			response.Result = &firestorepb.BatchGetDocumentsResponse_Found{Found: &firestorepb.Document{
				Name: name,
				Fields: map[string]*firestorepb.Value{
					"installation_id":  {ValueType: &firestorepb.Value_StringValue{StringValue: record.InstallationID}},
					"address":          {ValueType: &firestorepb.Value_StringValue{StringValue: record.Address}},
					"token_generation": {ValueType: &firestorepb.Value_IntegerValue{IntegerValue: record.TokenGeneration}},
					"state":            {ValueType: &firestorepb.Value_StringValue{StringValue: record.State}},
					"attempts":         {ValueType: &firestorepb.Value_IntegerValue{IntegerValue: record.Attempts}},
					"lease_until_ms":   {ValueType: &firestorepb.Value_IntegerValue{IntegerValue: record.LeaseUntilMs}},
					"lease_id":         {ValueType: &firestorepb.Value_StringValue{StringValue: record.LeaseID}},
					"expires_at_ms":    {ValueType: &firestorepb.Value_IntegerValue{IntegerValue: record.ExpiresAtMs}},
				},
				CreateTime: timestamppb.New(readTime),
				UpdateTime: timestamppb.New(readTime),
			}}
		default:
			response.Result = &firestorepb.BatchGetDocumentsResponse_Missing{Missing: name}
		}
		if err := stream.Send(response); err != nil {
			return err
		}
	}
	return nil
}

func (s *claimProbeFirestore) Commit(_ context.Context, request *firestorepb.CommitRequest) (*firestorepb.CommitResponse, error) {
	s.writeCounts = append(s.writeCounts, len(request.Writes))
	if s.abortFirst && len(request.Transaction) > 0 && request.Transaction[0] == 1 {
		return nil, status.Error(codes.Aborted, "forced retry")
	}
	return &firestorepb.CommitResponse{CommitTime: timestamppb.New(s.commitTime)}, nil
}

func (*claimProbeFirestore) Rollback(context.Context, *firestorepb.RollbackRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func newFirestoreTestClient(t *testing.T, implementation firestorepb.FirestoreServer) *firestore.Client {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	firestorepb.RegisterFirestoreServer(server, implementation)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client, err := firestore.NewClient(context.Background(), "demo-swarm-push-probe", option.WithGRPCConn(conn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func (s *retryFirestore) BeginTransaction(context.Context, *firestorepb.BeginTransactionRequest) (*firestorepb.BeginTransactionResponse, error) {
	s.attempt++
	return &firestorepb.BeginTransactionResponse{Transaction: []byte{byte(s.attempt)}}, nil
}

func (s *retryFirestore) BatchGetDocuments(request *firestorepb.BatchGetDocumentsRequest, stream firestorepb.Firestore_BatchGetDocumentsServer) error {
	attempt := int(request.GetTransaction()[0])
	return stream.Send(&firestorepb.BatchGetDocumentsResponse{
		Result: &firestorepb.BatchGetDocumentsResponse_Found{Found: &firestorepb.Document{
			Name: request.Documents[0],
			Fields: map[string]*firestorepb.Value{
				"count": {ValueType: &firestorepb.Value_IntegerValue{IntegerValue: int64(attempt - 1)}},
			},
			CreateTime: timestamppb.New(time.Unix(1, 0)),
			UpdateTime: timestamppb.New(time.Unix(int64(attempt), 0)),
		}},
		ReadTime: timestamppb.New(time.Unix(int64(attempt), 0)),
	})
}

func (s *retryFirestore) Commit(context.Context, *firestorepb.CommitRequest) (*firestorepb.CommitResponse, error) {
	if s.attempt == 1 {
		return nil, status.Error(codes.Aborted, "forced retry")
	}
	return &firestorepb.CommitResponse{CommitTime: timestamppb.New(time.Unix(3, 0))}, nil
}

func (*retryFirestore) Rollback(context.Context, *firestorepb.RollbackRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func TestPinnedFirestoreRetryRefreshesServerReadTime(t *testing.T) {
	client := newFirestoreTestClient(t, &retryFirestore{})

	var reads []time.Time
	err := client.RunTransaction(context.Background(), func(_ context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(client.Collection("retry").Doc("clock"))
		if err != nil {
			return err
		}
		reads = append(reads, snap.ReadTime)
		return tx.Update(snap.Ref, []firestore.Update{{Path: "count", Value: int64(len(reads))}})
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(reads) != 2 || !reads[1].After(reads[0]) {
		t.Fatalf("retry read times=%v, want two increasing server reads", reads)
	}
}

func TestFirestoreWakeClaimRejectsBadCapabilityAfterOneRead(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	server := &claimProbeFirestore{readTime: now, commitTime: now}
	repo := newFirestorePersistence(newFirestoreTestClient(t, server), "claim-probe")
	limit := RateLimit{Max: 10, Window: time.Minute}
	claim, claimed, err := repo.claimWake(context.Background(), "wake", "lease", "address", "wrong-cap-hash", now, now.Add(time.Minute), limit, "source", limit)
	if err != nil || claimed || claim.Denied != "unauthorized" {
		t.Fatalf("claim=%+v claimed=%v err=%v", claim, claimed, err)
	}
	if len(server.documents) != 1 || !strings.Contains(server.documents[0], "_addresses/") {
		t.Fatalf("bad capability read documents=%v, want only its address", server.documents)
	}
}

func TestFirestoreWakeClaimRejectsLeaseExpiredAtCommit(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	server := &claimProbeFirestore{readTime: now, commitTime: now.Add(wakeLease)}
	repo := newFirestorePersistence(newFirestoreTestClient(t, server), "claim-commit-fence")
	limit := RateLimit{Max: 10, Window: time.Minute}
	claim, claimed, err := repo.claimWake(context.Background(), "wake", "lease", "address", hashSecret("cap"), now.Add(-24*time.Hour), now.Add(time.Minute), limit, "source", limit)
	if err != nil || claimed || claim.Denied != "busy" {
		t.Fatalf("claim=%+v claimed=%v err=%v, want expired lease suppressed", claim, claimed, err)
	}
}

func TestFirestoreWakeClaimRetryResetsAttemptDeadline(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	server := &claimProbeFirestore{
		readTimes:  []time.Time{now, now.Add(40 * time.Second)},
		commitTime: now.Add(40 * time.Second),
		abortFirst: true,
		wakeRecords: map[int]*wakeAttemptRecord{1: {
			InstallationID: "installation",
			Address:        "address",
			State:          "claimed",
			Attempts:       1,
			ExpiresAtMs:    now.Add(30 * time.Second).UnixMilli(),
		}},
	}
	repo := newFirestorePersistence(newFirestoreTestClient(t, server), "claim-retry-deadline")
	limit := RateLimit{Max: 10, Window: time.Minute}
	claim, claimed, err := repo.claimWake(context.Background(), "wake", "lease", "address", hashSecret("cap"), now.Add(-24*time.Hour), now.Add(time.Minute), limit, "source", limit)
	if err != nil || !claimed || claim.ProviderBudget <= 0 {
		t.Fatalf("claim=%+v claimed=%v err=%v, want retry to restore the input deadline", claim, claimed, err)
	}
	if server.attempt != 2 {
		t.Fatalf("transaction attempts=%d, want forced retry", server.attempt)
	}
}

func TestFirestoreWakeClaimRetryUsesLatestReadTime(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	server := &claimProbeFirestore{readTimes: []time.Time{now, now.Add(2 * time.Minute)}, commitTime: now.Add(2 * time.Minute), abortFirst: true}
	repo := newFirestorePersistence(newFirestoreTestClient(t, server), "claim-retry-clock")
	limit := RateLimit{Max: 10, Window: time.Minute}
	claim, claimed, err := repo.claimWake(context.Background(), "wake", "lease", "address", hashSecret("cap"), now.Add(-24*time.Hour), now.Add(time.Minute), limit, "source", limit)
	if err != nil || claimed || claim.Denied != "malformed" || claim.ProviderBudget != 0 {
		t.Fatalf("claim=%+v claimed=%v err=%v, want retry expired at its latest server read", claim, claimed, err)
	}
	if server.attempt != 2 {
		t.Fatalf("transaction attempts=%d, want forced retry", server.attempt)
	}
}

func TestFirestoreWakeCompletionRetryUsesLatestReadTime(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	record := &wakeAttemptRecord{InstallationID: "installation", Address: "address", TokenGeneration: 1, State: "claimed", LeaseID: "lease", LeaseUntilMs: now.Add(time.Minute).UnixMilli(), ExpiresAtMs: now.Add(5 * time.Minute).UnixMilli()}
	server := &claimProbeFirestore{
		readTimes:   []time.Time{now, now.Add(2 * time.Minute)},
		commitTime:  now.Add(2 * time.Minute),
		abortFirst:  true,
		wakeRecords: map[int]*wakeAttemptRecord{1: record, 2: record},
	}
	repo := newFirestorePersistence(newFirestoreTestClient(t, server), "complete-retry-clock")
	stale, err := repo.completeWake(context.Background(), "wake", "lease", 1, 200, []byte("late"), true, now.Add(-24*time.Hour))
	if err != nil || stale {
		t.Fatalf("stale=%v err=%v", stale, err)
	}
	if server.attempt != 2 || len(server.writeCounts) != 2 || server.writeCounts[0] != 3 || server.writeCounts[1] != 0 {
		t.Fatalf("attempts=%d writes=%v, want aborted side effects followed by an expired no-op", server.attempt, server.writeCounts)
	}
}
