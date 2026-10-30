package client

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

func TestListLicenses(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v3/licenses" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`[
			{"key":"apache-2.0","name":"Apache License 2.0","spdx_id":"Apache-2.0","url":"u","node_id":"n"},
			{"key":"mit","name":"MIT License","spdx_id":"MIT","url":"u","node_id":"n"}
		]`))
	})

	licenses, err := c.ListLicenses(context.Background())
	if err != nil {
		t.Fatalf("ListLicenses: %v", err)
	}
	want := []LicenseSummary{
		{Key: "apache-2.0", Name: "Apache License 2.0", SPDXID: "Apache-2.0"},
		{Key: "mit", Name: "MIT License", SPDXID: "MIT"},
	}
	if !reflect.DeepEqual(licenses, want) {
		t.Errorf("licenses = %+v, want %+v", licenses, want)
	}
}

func TestGetLicense(t *testing.T) {
	for _, tc := range []struct{ key, path string }{
		{"mit", "/api/v3/licenses/mit"},
		{"MIT", "/api/v3/licenses/MIT"},
		{"a b/c?", "/api/v3/licenses/a%20b%2Fc%3F"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.EscapedPath() != tc.path {
					t.Errorf("got %s %s, want %s", r.Method, r.URL.EscapedPath(), tc.path)
				}
				_, _ = w.Write([]byte(`{"key":"mit","name":"MIT License","spdx_id":"MIT","node_id":"n",
					"html_url":"http://choosealicense.com/licenses/mit/","description":"Short.","implementation":"Copy it.",
					"permissions":["commercial-use"],"conditions":["include-copyright"],"limitations":["liability","warranty"],
					"body":"MIT License\n\nCopyright (c) [year] [fullname]\n","featured":true,"url":"u"}`))
			})

			l, err := c.GetLicense(context.Background(), tc.key)
			if err != nil {
				t.Fatalf("GetLicense: %v", err)
			}
			want := &License{
				LicenseSummary: LicenseSummary{Key: "mit", Name: "MIT License", SPDXID: "MIT"},
				HTMLURL:        "http://choosealicense.com/licenses/mit/",
				Description:    "Short.",
				Implementation: "Copy it.",
				Permissions:    []string{"commercial-use"},
				Conditions:     []string{"include-copyright"},
				Limitations:    []string{"liability", "warranty"},
				Body:           "MIT License\n\nCopyright (c) [year] [fullname]\n",
				Featured:       true,
			}
			if !reflect.DeepEqual(l, want) {
				t.Errorf("license = %+v, want %+v", l, want)
			}
		})
	}
}

func TestGetLicenseNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found","documentation_url":"https://docs.github.com/rest","status":"404"}`))
	})

	_, err := c.GetLicense(context.Background(), "isc")
	if !IsNotFound(err) {
		t.Fatalf("expected a not-found error, got %v", err)
	}
}
