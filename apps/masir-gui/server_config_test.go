package main

import (
	"bytes"
	"encoding/json"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestServerContextMenuOpensOnlyItsJSON(t *testing.T) {
	for _, page := range []string{"picker", "subscription", "target"} {
		t.Run(page, func(t *testing.T) {
			f := multiSubDaemon()
			raw := `{"remarks":"Pool","outbounds":[{"tag":"node","protocol":"vless","settings":{"address":"example.com"}}],"routing":{"balancers":[{"tag":"pool","selector":["node"]}],"rules":[{"balancerTag":"pool"}]},"observatory":{"subjectSelector":["node"]}}`
			f.sourceConfigJSON = raw
			f.subs[0].Configs[0].OutboundJson = `{"protocol":"freedom"}`
			a := newTestApp(f)
			switch page {
			case "picker":
				a.open = sectionServers
			case "subscription":
				a.openSubscription("s1")
			case "target":
				a.newRule()
				a.rulePage.expanded = "destination"
			}
			opened := ""
			openFileIn = func(path string) error { opened = path; return nil }
			t.Cleanup(func() {
				openFileIn = openFile
				if opened != "" {
					os.Remove(opened)
				}
			})
			tt := tester(a)
			tt.Advance(time.Second)
			action := "Use United Kingdom"
			if page == "target" {
				action = "Pin marzban United Kingdom"
			}
			if err := tt.RightClick(action); err != nil {
				t.Fatal(err)
			}
			tt.Advance(time.Second)
			if !a.menu.open || a.menu.config == nil {
				t.Fatal("config menu did not open")
			}
			if err := tt.Click("Open JSON"); err != nil {
				t.Fatal(err)
			}
			tt.Advance(time.Second)
			if !f.called("config json s1/c1") {
				t.Fatal("did not fetch the selected config")
			}
			if opened == "" {
				t.Fatal("JSON was not opened")
			}
			data, err := os.ReadFile(opened)
			if err != nil {
				t.Fatal(err)
			}
			var compact bytes.Buffer
			if err := json.Compact(&compact, data); err != nil {
				t.Fatal(err)
			}
			if compact.String() != raw || !bytes.Contains(data, []byte("\n  ")) {
				t.Fatalf("unexpected JSON: %s", data)
			}
			info, err := os.Stat(opened)
			if err != nil {
				t.Fatal(err)
			}
			// Windows reports synthesized Unix bits; access is controlled by ACLs.
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
				t.Fatalf("permissions: %v", info.Mode())
			}
			if f.called("get config") {
				t.Fatal("asked for the running config")
			}
		})
	}
}

func TestServerContextMenuRequiresOriginalJSON(t *testing.T) {
	a := newTestApp(multiSubDaemon())
	a.open = sectionServers
	opened := false
	openFileIn = func(string) error { opened = true; return nil }
	t.Cleanup(func() { openFileIn = openFile })
	tt := tester(a)
	if err := tt.RightClick("Use United Kingdom"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second)
	if err := tt.Click("Open JSON"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second)
	if opened {
		t.Fatal("opened a partial config without original JSON")
	}
	if !tt.HasText("Update the daemon and refresh this subscription to view its complete JSON config") {
		t.Fatal("missing subscription refresh guidance")
	}
}
