package netcalc

import "testing"

func TestCalcIPv4(t *testing.T) {
	r, err := CalcIPv4("192.168.10.77/27")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"network": r.Network, "broadcast": r.Broadcast, "first": r.FirstHost, "last": r.LastHost,
		"mask": r.Netmask, "wild": r.Wildcard,
	}
	exp := map[string]string{
		"network": "192.168.10.64", "broadcast": "192.168.10.95", "first": "192.168.10.65",
		"last": "192.168.10.94", "mask": "255.255.255.224", "wild": "0.0.0.31",
	}
	for k, v := range exp {
		if want[k] != v {
			t.Errorf("%s = %s, want %s", k, want[k], v)
		}
	}
	if r.Usable != 30 || r.Total != 32 || *r.AWSUsable != 27 || *r.OCIUsable != 29 {
		t.Errorf("contagens erradas: %+v", r)
	}
	if r.Type != "Privado (RFC 1918)" || r.Class != "C" {
		t.Errorf("tipo/classe: %s %s", r.Type, r.Class)
	}
}

func TestMaskFormats(t *testing.T) {
	for _, in := range []string{"10.1.2.3 255.255.240.0", "10.1.2.3/255.255.240.0", "10.1.2.3/20"} {
		r, err := CalcIPv4(in)
		if err != nil || r.Prefix != 20 || r.Network != "10.1.0.0" {
			t.Errorf("%s: %+v %v", in, r, err)
		}
	}
	if _, err := CalcIPv4("10.0.0.1/255.0.255.0"); err == nil {
		t.Error("máscara não contígua deveria falhar")
	}
	r, _ := CalcIPv4("10.0.0.0/31")
	if r.Usable != 2 {
		t.Errorf("/31 usable = %d", r.Usable)
	}
}

func TestSplitAndVLSM(t *testing.T) {
	subs, p, err := Split("10.0.0.0/16", 0, 6)
	if err != nil || p != 19 || len(subs) != 8 || subs[1].CIDR != "10.0.32.0/19" {
		t.Fatalf("split: p=%d n=%d %v", p, len(subs), err)
	}
	v, _, err := VLSM("192.168.1.0/24", []VLSMReq{{"a", 10}, {"b", 100}, {"c", 50}})
	if err != nil {
		t.Fatal(err)
	}
	if v[0].CIDR != "192.168.1.0/25" || v[1].CIDR != "192.168.1.128/26" || v[2].CIDR != "192.168.1.192/28" {
		t.Fatalf("vlsm: %+v", v)
	}
	if _, _, err := VLSM("192.168.1.0/24", []VLSMReq{{"x", 300}}); err == nil {
		t.Fatal("deveria faltar espaço")
	}
}

func TestMaskTableAndIPv6(t *testing.T) {
	tb := MaskTable()
	if len(tb) != 33 || tb[24].Netmask != "255.255.255.0" || tb[24].Wildcard != "0.0.0.255" || tb[30].Usable != 2 {
		t.Fatal("tabela de máscaras incorreta")
	}
	r, err := CalcIPv6("2001:db8:abcd::1/48")
	if err != nil || r.Network != "2001:db8:abcd::/48" || r.Subnets64 != "65536" || r.Last != "2001:db8:abcd:ffff:ffff:ffff:ffff:ffff" {
		t.Fatalf("ipv6: %+v %v", r, err)
	}
}
