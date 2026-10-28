package client

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestGetStorageUsage(t *testing.T) {
	gib2, gib5 := int64(2<<30), int64(5<<30)

	for _, tc := range []struct {
		name, owner, path, body string
		want                    StorageUsage
	}{
		{
			name: "quotas", owner: "alice", path: "/api/storage/alice",
			body: `{"usedBytes":12,"quotaBytes":2147483648,"maxAssetBytes":2147483648,` +
				`"fork":{"usedBytes":21023934,"quotaBytes":5368709120},"lfs":{"usedBytes":0,"quotaBytes":2147483648},` +
				`"asset":{"usedBytes":332169,"quotaBytes":2147483648}}`,
			want: StorageUsage{UsedBytes: 12, QuotaBytes: &gib2, MaxAssetBytes: gib2,
				Fork:  StorageLimitUsage{UsedBytes: 21023934, QuotaBytes: &gib5},
				LFS:   StorageLimitUsage{QuotaBytes: &gib2},
				Asset: StorageLimitUsage{UsedBytes: 332169, QuotaBytes: &gib2}},
		},
		{
			name: "no quotas", owner: "a b/c", path: "/api/storage/a%20b%2Fc",
			body: `{"usedBytes":1,"quotaBytes":null,"maxAssetBytes":2147483648,` +
				`"fork":{"usedBytes":2,"quotaBytes":null},"lfs":{"usedBytes":3,"quotaBytes":null},"asset":{"usedBytes":4,"quotaBytes":null}}`,
			want: StorageUsage{UsedBytes: 1, MaxAssetBytes: gib2,
				Fork: StorageLimitUsage{UsedBytes: 2}, LFS: StorageLimitUsage{UsedBytes: 3}, Asset: StorageLimitUsage{UsedBytes: 4}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.EscapedPath() != tc.path {
					t.Errorf("got %s %s, want GET %s", r.Method, r.URL.EscapedPath(), tc.path)
				}
				_, _ = w.Write([]byte(tc.body))
			})

			got, err := c.GetStorageUsage(context.Background(), tc.owner)
			if err != nil {
				t.Fatalf("GetStorageUsage: %v", err)
			}
			if !reflect.DeepEqual(*got, tc.want) {
				t.Errorf("got %+v, want %+v", *got, tc.want)
			}
		})
	}
}

func TestGetStorageUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		status   int
		message  string
		notFound bool
	}{
		{http.StatusNotFound, "User bob not found", true},
		{http.StatusUnauthorized, "Unauthorized", false},
		{http.StatusInternalServerError, "boom", false},
	} {
		t.Run(tc.message, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprintf(w, `{"statusCode":%d,"message":%q}`, tc.status, tc.message)
			})

			got, err := c.GetStorageUsage(context.Background(), "bob")
			if got != nil || err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("got %v, %v, want an error with %q", got, err, tc.message)
			}
			if IsNotFound(err) != tc.notFound {
				t.Errorf("IsNotFound = %v for %v", IsNotFound(err), err)
			}
		})
	}
}
