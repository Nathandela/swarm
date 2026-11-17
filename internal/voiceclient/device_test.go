package voiceclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/voicewire"
)

type deviceRoundTripFunc func(*http.Request) (*http.Response, error)

func (f deviceRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestDevicePublicationsBoundsAggregateJSON(t *testing.T) {
	_, signer, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := json.Marshal(voicewire.DevicePublication{ID: "phrase", Sequence: 1})
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Repeat("x", 64<<10)
	second := strings.Repeat("y", (128<<10)-len(first)-2*len(empty)-len("[,]"))
	for _, tc := range []struct {
		name string
		text []string
		want int
	}{
		{"exact capacity", []string{first, second}, 2},
		{"one more phrase", []string{first, second, "extra"}, 0},
		{"escaped contents", []string{strings.Repeat("&", 24<<10)}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index := 0
			transport := deviceRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				var body any = voicewire.DeviceChallenge{Challenge: strings.Repeat("0", 64)}
				if req.URL.Path == "/v1/device/publications" {
					var page voicewire.DevicePublicationPage
					if index < len(tc.text) {
						index++
						page = voicewire.DevicePublicationPage{
							Item: &voicewire.DevicePublication{ID: "phrase", Sequence: uint64(index)},
							Text: []byte(tc.text[index-1]),
						}
					}
					body = page
				}
				raw, err := json.Marshal(body)
				if err != nil {
					return nil, err
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw))}, nil
			})
			client := &DeviceClient{http: &http.Client{Transport: transport}, signer: signer, gate: make(chan struct{}, 1)}
			publications, err := client.Publications(context.Background(), voicewire.DeviceCredential{}, "utterance")
			if tc.want != 0 && (err != nil || len(publications) != tc.want) {
				t.Fatalf("exact capacity = %d publications, %v", len(publications), err)
			}
			if tc.want == 0 && (!errors.Is(err, ErrPublicationCapacity) || publications != nil) {
				t.Fatalf("overflow = %d publications, %v", len(publications), err)
			}
		})
	}
}
