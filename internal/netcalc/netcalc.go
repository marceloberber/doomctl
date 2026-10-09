// Package netcalc: calculadora de sub-redes IPv4/IPv6, divisão (subnetting),
// VLSM e tabela de máscaras/wildcard. Implementação pura em Go (net/netip).
package netcalc

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

type IPv4Result struct {
	Input        string `json:"input"`
	Address      string `json:"address"`
	Prefix       int    `json:"prefix"`
	Netmask      string `json:"netmask"`
	Wildcard     string `json:"wildcard"`
	Network      string `json:"network"`
	Broadcast    string `json:"broadcast"`
	FirstHost    string `json:"first_host"`
	LastHost     string `json:"last_host"`
	Total        uint64 `json:"total"`
	Usable       uint64 `json:"usable"`
	AWSUsable    *int64 `json:"aws_usable"` // AWS reserva 5 IPs (subnets /16–/28)
	OCIUsable    *int64 `json:"oci_usable"` // OCI reserva 3 IPs (subnets /16–/30)
	Class        string `json:"class"`
	Type         string `json:"type"`
	CIDR         string `json:"cidr"`
	BinaryAddr   string `json:"binary_address"`
	BinaryMask   string `json:"binary_netmask"`
	HexAddr      string `json:"hex_address"`
	IntAddr      uint32 `json:"int_address"`
	ReverseZone  string `json:"reverse_zone"`
	HostRangeTip string `json:"note,omitempty"`
}

// ParseIPv4 aceita "10.0.0.1/24", "10.0.0.1 255.255.255.0", "10.0.0.1/255.255.255.0" ou "10.0.0.1" (/32).
func ParseIPv4(in string) (netip.Addr, int, error) {
	s := strings.TrimSpace(in)
	var addrS, maskS string
	switch {
	case strings.Contains(s, "/"):
		parts := strings.SplitN(s, "/", 2)
		addrS, maskS = parts[0], parts[1]
	case strings.Contains(s, " "):
		f := strings.Fields(s)
		if len(f) != 2 {
			return netip.Addr{}, 0, errors.New("formato inválido")
		}
		addrS, maskS = f[0], f[1]
	default:
		addrS, maskS = s, "32"
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(addrS))
	if err != nil || !addr.Is4() {
		return netip.Addr{}, 0, fmt.Errorf("endereço IPv4 inválido: %q", addrS)
	}
	maskS = strings.TrimSpace(maskS)
	if strings.Contains(maskS, ".") {
		p, err := MaskToPrefix(maskS)
		if err != nil {
			return netip.Addr{}, 0, err
		}
		return addr, p, nil
	}
	p, err := strconv.Atoi(maskS)
	if err != nil || p < 0 || p > 32 {
		return netip.Addr{}, 0, fmt.Errorf("prefixo inválido: %q", maskS)
	}
	return addr, p, nil
}

func MaskToPrefix(mask string) (int, error) {
	m, err := netip.ParseAddr(mask)
	if err != nil || !m.Is4() {
		return 0, fmt.Errorf("máscara inválida: %q", mask)
	}
	v := u32(m)
	ones := 0
	for i := 31; i >= 0 && v&(1<<uint(i)) != 0; i-- {
		ones++
	}
	if ones < 32 && v<<uint(ones) != 0 {
		return 0, fmt.Errorf("máscara não contígua: %q", mask)
	}
	return ones, nil
}

func u32(a netip.Addr) uint32 { b := a.As4(); return binary.BigEndian.Uint32(b[:]) }
func fromU32(v uint32) netip.Addr {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return netip.AddrFrom4(b)
}
func maskU32(p int) uint32 {
	if p == 0 {
		return 0
	}
	return ^uint32(0) << uint(32-p)
}

func bin(v uint32) string {
	s := fmt.Sprintf("%032b", v)
	return s[0:8] + "." + s[8:16] + "." + s[16:24] + "." + s[24:32]
}

func CalcIPv4(in string) (*IPv4Result, error) {
	addr, p, err := ParseIPv4(in)
	if err != nil {
		return nil, err
	}
	return calc(in, addr, p), nil
}

func calc(in string, addr netip.Addr, p int) *IPv4Result {
	a := u32(addr)
	m := maskU32(p)
	net := a & m
	bc := net | ^m
	total := uint64(1) << uint(32-p)
	r := &IPv4Result{
		Input: in, Address: addr.String(), Prefix: p,
		Netmask: fromU32(m).String(), Wildcard: fromU32(^m).String(),
		Network: fromU32(net).String(), Broadcast: fromU32(bc).String(),
		Total: total, CIDR: fmt.Sprintf("%s/%d", fromU32(net), p),
		BinaryAddr: bin(a), BinaryMask: bin(m), HexAddr: fmt.Sprintf("0x%08X", a), IntAddr: a,
		Class: class(a), Type: addrType(addr),
	}
	switch p {
	case 32:
		r.FirstHost, r.LastHost, r.Usable = addr.String(), addr.String(), 1
		r.Broadcast = "—"
		r.HostRangeTip = "/32: rota de host único"
	case 31:
		r.FirstHost, r.LastHost, r.Usable = fromU32(net).String(), fromU32(bc).String(), 2
		r.Broadcast = "—"
		r.HostRangeTip = "/31: enlace ponto-a-ponto (RFC 3021), sem rede/broadcast"
	default:
		r.FirstHost, r.LastHost, r.Usable = fromU32(net+1).String(), fromU32(bc-1).String(), total-2
	}
	if p >= 16 && p <= 28 {
		v := int64(total) - 5
		r.AWSUsable = &v
	}
	if p >= 16 && p <= 30 {
		v := int64(total) - 3
		r.OCIUsable = &v
	}
	octets := strings.Split(fromU32(net).String(), ".")
	switch {
	case p >= 24:
		r.ReverseZone = fmt.Sprintf("%s.%s.%s.in-addr.arpa", octets[2], octets[1], octets[0])
	case p >= 16:
		r.ReverseZone = fmt.Sprintf("%s.%s.in-addr.arpa", octets[1], octets[0])
	case p >= 8:
		r.ReverseZone = fmt.Sprintf("%s.in-addr.arpa", octets[0])
	}
	return r
}

func class(a uint32) string {
	f := a >> 24
	switch {
	case f < 128:
		return "A"
	case f < 192:
		return "B"
	case f < 224:
		return "C"
	case f < 240:
		return "D (multicast)"
	default:
		return "E (reservada)"
	}
}

var special = []struct {
	pfx  netip.Prefix
	name string
}{
	{netip.MustParsePrefix("10.0.0.0/8"), "Privado (RFC 1918)"},
	{netip.MustParsePrefix("172.16.0.0/12"), "Privado (RFC 1918)"},
	{netip.MustParsePrefix("192.168.0.0/16"), "Privado (RFC 1918)"},
	{netip.MustParsePrefix("100.64.0.0/10"), "CGNAT (RFC 6598)"},
	{netip.MustParsePrefix("127.0.0.0/8"), "Loopback"},
	{netip.MustParsePrefix("169.254.0.0/16"), "Link-local (APIPA)"},
	{netip.MustParsePrefix("0.0.0.0/8"), "\"Esta\" rede"},
	{netip.MustParsePrefix("192.0.2.0/24"), "Documentação (TEST-NET-1)"},
	{netip.MustParsePrefix("198.51.100.0/24"), "Documentação (TEST-NET-2)"},
	{netip.MustParsePrefix("203.0.113.0/24"), "Documentação (TEST-NET-3)"},
	{netip.MustParsePrefix("198.18.0.0/15"), "Benchmark (RFC 2544)"},
	{netip.MustParsePrefix("224.0.0.0/4"), "Multicast"},
	{netip.MustParsePrefix("240.0.0.0/4"), "Reservado"},
}

func addrType(a netip.Addr) string {
	for _, s := range special {
		if s.pfx.Contains(a) {
			return s.name
		}
	}
	return "Público"
}

// ---------- Divisão de rede ----------

type Subnet struct {
	Name      string `json:"name,omitempty"`
	Needed    int    `json:"needed,omitempty"`
	CIDR      string `json:"cidr"`
	Network   string `json:"network"`
	FirstHost string `json:"first_host"`
	LastHost  string `json:"last_host"`
	Broadcast string `json:"broadcast"`
	Netmask   string `json:"netmask"`
	Usable    uint64 `json:"usable"`
}

const maxSubnets = 4096

func toSubnet(net uint32, p int) Subnet {
	r := calc("", fromU32(net), p)
	return Subnet{CIDR: r.CIDR, Network: r.Network, FirstHost: r.FirstHost, LastHost: r.LastHost,
		Broadcast: r.Broadcast, Netmask: r.Netmask, Usable: r.Usable}
}

// Split divide a rede em sub-redes de newPrefix (ou na quantidade "count").
func Split(cidr string, newPrefix, count int) ([]Subnet, int, error) {
	addr, p, err := ParseIPv4(cidr)
	if err != nil {
		return nil, 0, err
	}
	if count > 0 {
		bits := 0
		for (1 << uint(bits)) < count {
			bits++
		}
		newPrefix = p + bits
	}
	if newPrefix < p || newPrefix > 32 {
		return nil, 0, fmt.Errorf("novo prefixo /%d inválido para uma rede /%d", newPrefix, p)
	}
	n := 1 << uint(newPrefix-p)
	if n > maxSubnets {
		return nil, 0, fmt.Errorf("resultaria em %d sub-redes (máximo %d)", n, maxSubnets)
	}
	base := u32(addr) & maskU32(p)
	step := uint32(1) << uint(32-newPrefix)
	out := make([]Subnet, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, toSubnet(base+uint32(i)*step, newPrefix))
	}
	return out, newPrefix, nil
}

type VLSMReq struct {
	Name  string `json:"name"`
	Hosts int    `json:"hosts"`
}

// VLSM aloca sub-redes do maior para o menor requisito.
func VLSM(cidr string, reqs []VLSMReq) ([]Subnet, string, error) {
	addr, p, err := ParseIPv4(cidr)
	if err != nil {
		return nil, "", err
	}
	if len(reqs) == 0 || len(reqs) > 256 {
		return nil, "", errors.New("informe de 1 a 256 sub-redes")
	}
	sorted := append([]VLSMReq(nil), reqs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Hosts > sorted[j].Hosts })
	base := u32(addr) & maskU32(p)
	end := uint64(base) + (uint64(1) << uint(32-p))
	cur := uint64(base)
	var out []Subnet
	for _, r := range sorted {
		if r.Hosts < 1 {
			return nil, "", fmt.Errorf("sub-rede %q: quantidade de hosts inválida", r.Name)
		}
		need := uint64(r.Hosts) + 2
		hb := 0
		for (uint64(1) << uint(hb)) < need {
			hb++
		}
		if hb < 2 {
			hb = 2
		}
		np := 32 - hb
		if np < p {
			return nil, "", fmt.Errorf("sub-rede %q (%d hosts) não cabe em %s", r.Name, r.Hosts, cidr)
		}
		size := uint64(1) << uint(hb)
		if cur%size != 0 { // alinhamento
			cur += size - cur%size
		}
		if cur+size > end {
			return nil, "", fmt.Errorf("espaço insuficiente em %s para %q", cidr, r.Name)
		}
		s := toSubnet(uint32(cur), np)
		s.Name, s.Needed = r.Name, r.Hosts
		out = append(out, s)
		cur += size
	}
	free := end - cur
	return out, fmt.Sprintf("%d endereços livres após a última alocação", free), nil
}

// ---------- Tabela de máscaras ----------

type MaskRow struct {
	Prefix   int    `json:"prefix"`
	Netmask  string `json:"netmask"`
	Wildcard string `json:"wildcard"`
	Hex      string `json:"hex"`
	Total    uint64 `json:"total"`
	Usable   uint64 `json:"usable"`
	Classful string `json:"classful"`
}

func MaskTable() []MaskRow {
	rows := make([]MaskRow, 0, 33)
	for p := 0; p <= 32; p++ {
		m := maskU32(p)
		total := uint64(1) << uint(32-p)
		usable := total - 2
		switch p {
		case 31:
			usable = 2
		case 32:
			usable = 1
		}
		cf := ""
		switch {
		case p == 8:
			cf = "Classe A"
		case p == 16:
			cf = "Classe B"
		case p == 24:
			cf = "Classe C"
		case p > 24:
			cf = fmt.Sprintf("1/%d de C", 1<<uint(p-24))
		case p > 16:
			cf = fmt.Sprintf("%d × C", 1<<uint(24-p))
		case p > 8:
			cf = fmt.Sprintf("%d × B", 1<<uint(16-p))
		default:
			cf = fmt.Sprintf("%d × A", 1<<uint(8-p))
		}
		rows = append(rows, MaskRow{Prefix: p, Netmask: fromU32(m).String(), Wildcard: fromU32(^m).String(),
			Hex: fmt.Sprintf("0x%08X", m), Total: total, Usable: usable, Classful: cf})
	}
	return rows
}

// ---------- IPv6 (noções) ----------

type IPv6Result struct {
	Input     string `json:"input"`
	Address   string `json:"address"`
	Expanded  string `json:"expanded"`
	Prefix    int    `json:"prefix"`
	Network   string `json:"network"`
	First     string `json:"first"`
	Last      string `json:"last"`
	Total     string `json:"total"`
	Subnets64 string `json:"subnets_64,omitempty"`
	Type      string `json:"type"`
}

func CalcIPv6(in string) (*IPv6Result, error) {
	s := strings.TrimSpace(in)
	if !strings.Contains(s, "/") {
		s += "/128"
	}
	pfx, err := netip.ParsePrefix(s)
	if err != nil || !pfx.Addr().Is6() || pfx.Addr().Is4In6() {
		return nil, fmt.Errorf("prefixo IPv6 inválido: %q", in)
	}
	p := pfx.Bits()
	netw := pfx.Masked().Addr()
	nb := netw.As16()
	lb := nb
	for i := p; i < 128; i++ {
		lb[i/8] |= 1 << uint(7-i%8)
	}
	total := new(big.Int).Lsh(big.NewInt(1), uint(128-p))
	r := &IPv6Result{Input: in, Address: pfx.Addr().String(), Expanded: expand6(pfx.Addr()), Prefix: p,
		Network: netw.String() + "/" + strconv.Itoa(p), First: netw.String(), Last: netip.AddrFrom16(lb).String(),
		Total: total.String()}
	if p <= 64 {
		r.Subnets64 = new(big.Int).Lsh(big.NewInt(1), uint(64-p)).String()
	}
	a := pfx.Addr()
	switch {
	case a.IsLoopback():
		r.Type = "Loopback"
	case a.IsLinkLocalUnicast():
		r.Type = "Link-local (fe80::/10)"
	case a.IsMulticast():
		r.Type = "Multicast"
	case a.IsPrivate():
		r.Type = "ULA (fc00::/7)"
	case netip.MustParsePrefix("2001:db8::/32").Contains(a):
		r.Type = "Documentação"
	case a.IsGlobalUnicast():
		r.Type = "Global unicast"
	default:
		r.Type = "Especial"
	}
	return r, nil
}

func expand6(a netip.Addr) string {
	b := a.As16()
	parts := make([]string, 8)
	for i := 0; i < 8; i++ {
		parts[i] = fmt.Sprintf("%02x%02x", b[2*i], b[2*i+1])
	}
	return strings.Join(parts, ":")
}
