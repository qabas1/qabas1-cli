package compose

import "testing"

func TestServiceAlias(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"all", ""},
		{"api", "api"},
		{"operations", "operations-worker"},
		{"operations-worker", "operations-worker"},
		{"ops", "operations-worker"},
		{"posting", "operations-worker"},
		{"post", "operations-worker"},
		{"posting-worker", "operations-worker"},
		{"notifications", "notifications-worker"},
		{"printables", "printables-worker"},
		{"jsreport", "jsreport"},
		{"audit", "audit-cleanup"},
	}
	for _, tc := range cases {
		got, err := ServiceAlias(tc.in)
		if err != nil {
			t.Fatalf("ServiceAlias(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ServiceAlias(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestProductService(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"api", "api", true},
		{"dashboard", "dash", true},
		{"dash", "dash", true},
		{"postgres", "", false},
		{"operations", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := ProductService(tc.in)
		if ok != tc.wantOK || got != tc.want {
			t.Fatalf("ProductService(%q)=(%q,%v), want (%q,%v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestDeployComposeServices(t *testing.T) {
	t.Parallel()
	if got := DeployComposeServices("app"); len(got) != 1 || got[0] != "app" {
		t.Fatalf("app services %#v", got)
	}
	want := []string{
		"api",
		"printables-worker",
		"operations-worker",
		"notifications-worker",
		"audit-cleanup",
	}
	got := DeployComposeServices("api")
	if len(got) != len(want) {
		t.Fatalf("api services %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("api services %#v, want %#v", got, want)
		}
	}
}
