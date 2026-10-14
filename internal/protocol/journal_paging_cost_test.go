package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"
)

func TestJournalPaginationWorkIsBoundedByPageSize(t *testing.T) {
	for _, kind := range []string{"journal", "roster"} {
		t.Run(kind, func(t *testing.T) {
			records := make([]JournalRecord, 2048)
			for i := range records {
				records[i] = JournalRecord{Cursor: uint64(i + 1), SessionID: strings.Repeat("x", 512), Type: "interaction"}
			}
			reply := Control{Op: OpJournalSubscribeFrom, Cursor: uint64(len(records))}
			if kind == "journal" {
				reply.Journal = records
			} else {
				reply.Roster = records
			}
			_, _ = EncodeControl(Control{Op: OpJournalSubscribeFrom})
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			pages, err := journalReadPages(reply, 16<<10, nil)
			runtime.ReadMemStats(&after)
			if err != nil {
				t.Fatal(err)
			}
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 32<<20 {
				t.Fatalf("paging a 1 MiB %s backlog allocated %d bytes; page sizing must not repeatedly encode the remaining backlog", kind, allocated)
			}
			t.Logf("paging allocated %d bytes", after.TotalAlloc-before.TotalAlloc)
			var got []JournalRecord
			for i, page := range pages {
				body, err := EncodeControl(page)
				if err != nil || len(body) > 16<<10 || page.Cursor != reply.Cursor || page.JournalMore != (i+1 < len(pages)) {
					t.Fatalf("page %d lost its byte bound, cursor or continuation: size=%d err=%v", i, len(body), err)
				}
				if kind == "journal" {
					got = append(got, page.Journal...)
				} else {
					got = append(got, page.Roster...)
				}
			}
			if len(got) != len(records) {
				t.Fatalf("replayed %d of %d records", len(got), len(records))
			}
			for i, rec := range got {
				if rec.Cursor != records[i].Cursor {
					t.Fatalf("record %d was lost or reordered", i)
				}
			}
		})
	}
}

func TestJournalPaginationStopsWhenConnectionCloses(t *testing.T) {
	done := make(chan struct{})
	close(done)
	pages, err := journalReadPages(Control{Op: OpJournalSubscribeFrom, Journal: []JournalRecord{{Cursor: 1, SessionID: "task"}}}, 16<<10, done)
	if !errors.Is(err, context.Canceled) || len(pages) != 0 {
		t.Fatalf("closed connection still paged history: pages=%d err=%v", len(pages), err)
	}
}

func TestJournalPaginationPreservesMixedEscapedRecords(t *testing.T) {
	journal := []JournalRecord{{Cursor: 1, SessionID: "quotes\" and \n unicode 世界"}, {Cursor: 2, SessionID: "<&>"}}
	journal[0].Item = json.RawMessage(`{ "text": "<>& \"quoted\" 世界", "nested": ["\\", "line\nbreak"] }`)
	roster := []JournalRecord{{Cursor: 2, SessionID: "roster\""}}
	reply := Control{Op: OpJournalSubscribeFrom, Cursor: 2, FullResync: true, Journal: journal, Roster: roster}
	for _, limit := range []int{350, 512, 1 << 20, 2 << 20} {
		pages, err := journalReadPages(reply, limit, nil)
		if err != nil {
			t.Fatal(err)
		}
		var events, rows []JournalRecord
		for _, page := range pages {
			body, err := EncodeControl(page)
			if err != nil || len(body) > min(limit, (1<<20)-1) || !page.FullResync || page.Cursor != 2 {
				t.Fatal("mixed page lost frame bound or snapshot metadata")
			}
			page, err = DecodeControl(body)
			if err != nil {
				t.Fatal(err)
			}
			events = append(events, page.Journal...)
			rows = append(rows, page.Roster...)
		}
		if len(events) != 2 || len(rows) != 1 || events[0].SessionID != journal[0].SessionID || events[1].SessionID != journal[1].SessionID || rows[0].SessionID != roster[0].SessionID {
			t.Fatal("mixed escaped records were lost or altered")
		}
		var item struct{ Text string }
		if err := json.Unmarshal(events[0].Item, &item); err != nil || item.Text != "<>& \"quoted\" 世界" {
			t.Fatal("raw interaction content was altered")
		}
	}
}
