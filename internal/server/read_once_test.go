package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/masanami/flywheel/internal/core"
)

// masanami/flywheel#837 の、課題の詳細と承認の API の読み取りの試験。

// startServerWithConfig は cfg を渡して server を立てる（ワークスペース名は w）。
func startServerWithConfig(t *testing.T, ws string, cfg eventsConfig) int {
	t.Helper()
	ln, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(ln, []core.FleetWorkspace{{Name: "w", Path: ws}}, cfg)
	errc := make(chan error, 1)
	go func() { errc <- s.Serve() }()
	t.Cleanup(func() {
		_ = s.Shutdown()
		<-errc
	})
	return ln.Addr().(*net.TCPAddr).Port
}

// 承認が確定した後のツリーの読み直しが失敗しても、承認の API は成功を返し、tree だけが null になる。承認の記録は 1 件。
func TestApproveSucceedsWhenTreeRereadFails_837(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		prep         func(t *testing.T, ws string)
	}{
		{"plan", "awaiting_plan_approval", func(*testing.T, string) {}},
		{"completion", "awaiting_completion_approval", func(t *testing.T, ws string) { addOp(t, ws, "release", "rel", strp("v1")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := initWorkspace(t)
			approvalFixture(t, ws, tc.status)
			tc.prep(t, ws)
			var logged []string
			var mu sync.Mutex
			port := startServerWithConfig(t, ws, eventsConfig{
				readTreeNode: func(context.Context, *core.Store, string, time.Time, *core.Overview) (*core.TreeChallenge, error) {
					return nil, errors.New("injected")
				},
				logf: func(f string, a ...any) { mu.Lock(); logged = append(logged, fmt.Sprintf(f, a...)); mu.Unlock() },
			})
			v := currentVersion(t, port)
			code, body := apiPost(t, port, approvePath, `{"version":`+strconv.Itoa(v)+`}`)
			if code != 200 {
				t.Fatalf("%d %s", code, body)
			}
			doc := map[string]json.RawMessage{}
			if err := json.Unmarshal([]byte(body), &doc); err != nil {
				t.Fatal(err)
			}
			if string(doc["tree"]) != "null" {
				t.Errorf("tree = %s, want null", doc["tree"])
			}
			for _, k := range []string{"approvals", "card", "challenge", "cycle", "holds", "plans", "runs"} {
				if _, ok := doc[k]; !ok {
					t.Errorf("response lacks %q", k)
				}
			}
			d := showApprovals(t, ws)
			n := 0
			for _, a := range d.Approvals {
				if a.Decision == core.ApprovalDecisionApproved {
					n++
				}
			}
			if n != 1 {
				t.Errorf("approved records = %d, want 1: %+v", n, d.Approvals)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(logged) != 1 || !strings.Contains(logged[0], "injected") {
				t.Errorf("log = %v", logged)
			}
		})
	}
}

// 承認が確定しなかった（状態が対象外）ときのエラーは、いまのまま返す。
func TestApproveErrorUnchangedWhenNotApproved_837(t *testing.T) {
	ws := initWorkspace(t)
	approvalFixture(t, ws, "in_progress")
	port := startServerWithConfig(t, ws, eventsConfig{})
	if code, body := apiPost(t, port, approvePath, `{"version":1}`); code != 409 || errCode(t, body) != "invalid_transition" {
		t.Errorf("%d %s", code, body)
	}
}

// 1 回の要求で、状況（Overview）の組み立ては 1 回。承認では確定の後の 1 回で、応答のカードに確定後の値が出る。
func TestOverviewBuiltOncePerRequest_837(t *testing.T) {
	var calls int32
	cfg := eventsConfig{buildOverview: func(ctx context.Context, st *core.Store) (*core.Overview, error) {
		atomic.AddInt32(&calls, 1)
		return baseOverview(ctx, st)
	}}
	t.Run("challenge detail", func(t *testing.T) {
		ws := initWorkspace(t)
		approvalFixture(t, ws, "awaiting_plan_approval")
		port := startServerWithConfig(t, ws, cfg)
		atomic.StoreInt32(&calls, 0)
		if code, body := apiGet(t, port, "/api/v1/workspaces/w/challenges/C-1"); code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		if n := atomic.LoadInt32(&calls); n != 1 {
			t.Errorf("overview built %d times, want 1", n)
		}
	})
	t.Run("approve", func(t *testing.T) {
		ws := initWorkspace(t)
		approvalFixture(t, ws, "awaiting_plan_approval")
		port := startServerWithConfig(t, ws, cfg)
		v := currentVersion(t, port)
		atomic.StoreInt32(&calls, 0)
		code, body := apiPost(t, port, approvePath, `{"version":`+strconv.Itoa(v)+`}`)
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		if n := atomic.LoadInt32(&calls); n != 1 {
			t.Errorf("overview built %d times, want 1", n)
		}
		var doc struct {
			Challenge struct{ Status string } `json:"challenge"`
			Tree      struct{ Status string } `json:"tree"`
		}
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Challenge.Status != "in_progress" || doc.Tree.Status != "in_progress" {
			t.Errorf("not the post-approval state: %s", body)
		}
	})
}
