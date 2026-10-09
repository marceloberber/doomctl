package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/doomctl/doomctl/internal/netcalc"
)

// ---------- calculadora de sub-redes ----------

func (s *Server) netCalc(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct{ Input string }
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	in.Input = strings.TrimSpace(in.Input)
	var res any
	var err error
	if strings.Contains(in.Input, ":") {
		res, err = netcalc.CalcIPv6(in.Input)
	} else {
		res, err = netcalc.CalcIPv4(in.Input)
	}
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	s.logCalc(u, "calc", in.Input, res)
	writeJSON(w, 200, res)
}

func (s *Server) logCalc(u *User, action, target string, res any) {
	b, _ := json.MarshalIndent(res, "", "  ")
	go s.audit("netcalc", action, truncate(target, 200), u, "info", truncate(string(b), 64<<10), map[string]string{"input": target})
}

func (s *Server) netSplit(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		Network string `json:"network"`
		Prefix  int    `json:"prefix"`
		Count   int    `json:"count"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	subs, p, err := netcalc.Split(in.Network, in.Prefix, in.Count)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	res := map[string]any{"prefix": p, "subnets": subs}
	s.logCalc(u, "split", fmt.Sprintf("%s → /%d", in.Network, p), map[string]any{"prefix": p, "count": len(subs)})
	writeJSON(w, 200, res)
}

func (s *Server) netVLSM(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		Network  string            `json:"network"`
		Requests []netcalc.VLSMReq `json:"requests"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	subs, note, err := netcalc.VLSM(in.Network, in.Requests)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	s.logCalc(u, "vlsm", in.Network, subs)
	writeJSON(w, 200, map[string]any{"subnets": subs, "note": note})
}

func (s *Server) netMasks(w http.ResponseWriter, r *http.Request, _ *User) {
	writeJSON(w, 200, netcalc.MaskTable())
}
