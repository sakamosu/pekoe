package daemon

// The local GUI's Dashboard data beyond the phone's /api/* (docs/gui.md の
// 段階の表、docs/web.md の API の表): the merges per day and today's
// tokens (stats), and the ledger (ledger.list). Thin handlers on the GUI's
// mux only: each calls the method the CLI calls, as the person (the GUI's
// Caller), and shapes nothing but how much comes back.

import (
	"net/http"
	"sort"
	"strconv"

	"pekoe/internal/state"
)

const (
	guiStatsDays     = 7   // the Overview's days (today included), as the TUI's dashboard
	guiLedgerClosed  = 5   // closed requests under the open ones, by default
	guiLedgerMaxRead = 500 // closed requests one read may ask for (Open ledger)
)

// guiStats is GET /api/stats: `pekoe --json stats` for the last
// guiStatsDays days (docs/api.md「集計(stats)」), unchanged.
func (d *Daemon) guiStats(w http.ResponseWriter, r *http.Request) {
	res, err := d.CallAs(humanCaller(webVia(peerFrom(r))), "stats", mustJSON(map[string]any{"days": guiStatsDays}))
	if err != nil {
		webErr(w, err)
		return
	}
	writeJSON(w, res)
}

// guiLedger is GET /api/ledger?closed=N: every open request (oldest
// first, as ledger.list), then the N closed ones closed last (newest
// first; default guiLedgerClosed, at most guiLedgerMaxRead), and how many
// are closed in all. The texts are the person's words and, for what
// pekoed closed itself, nothing an agent wrote; the page draws them as
// text all the same.
func (d *Daemon) guiLedger(w http.ResponseWriter, r *http.Request) {
	n := guiLedgerClosed
	if q := r.URL.Query().Get("closed"); q != "" {
		v, err := strconv.Atoi(q)
		if err != nil || v < 0 || v > guiLedgerMaxRead {
			http.Error(w, "closed must be 0 to "+strconv.Itoa(guiLedgerMaxRead), http.StatusBadRequest)
			return
		}
		n = v
	}
	res, err := d.CallAs(humanCaller(webVia(peerFrom(r))), "ledger.list", mustJSON(ledgerListP{Status: "all"}))
	if err != nil {
		webErr(w, err)
		return
	}
	all, _ := res.([]state.LedgerEntry)
	out := guiLedgerView{Open: []state.LedgerEntry{}, Closed: []state.LedgerEntry{}}
	for _, e := range all {
		if e.Status == state.LedgerOpen {
			out.Open = append(out.Open, e)
		} else {
			out.Closed = append(out.Closed, e)
		}
	}
	closedAt := func(e state.LedgerEntry) int64 {
		if e.ClosedAt != nil {
			return e.ClosedAt.UnixNano()
		}
		return e.At.UnixNano()
	}
	sort.SliceStable(out.Closed, func(i, j int) bool {
		a, b := out.Closed[i], out.Closed[j]
		if ca, cb := closedAt(a), closedAt(b); ca != cb {
			return ca > cb
		}
		return a.ID > b.ID
	})
	out.ClosedTotal = len(out.Closed)
	out.Closed = out.Closed[:min(n, len(out.Closed))]
	writeJSON(w, out)
}

type guiLedgerView struct {
	Open        []state.LedgerEntry `json:"open"`
	Closed      []state.LedgerEntry `json:"closed"`
	ClosedTotal int                 `json:"closed_total"`
}
