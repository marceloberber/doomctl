package finops

import (
	"testing"

	"github.com/doomctl/doomctl/internal/gen"
)

// A estimativa precisa entender o código gerado pelo módulo OpenTofu do doomctl.
func TestIaCOnDoomctlGenerator(t *testing.T) {
	_, dd := demoData()
	prices := NewPrices(dd.Prices)
	st := DefaultSettings()
	aws, err := gen.Tofu(gen.TofuSpec{Provider: "aws", Name: "web", Region: "us-east-1", Tags: "Project=portal\nEnvironment=dev\nOwner=web",
		Network: true, NetCIDR: "10.0.0.0/16", SubnetCIDR: "10.0.1.0/24", SSHCIDR: "10.0.0.0/8", AZ: "us-east-1a", Compute: true, InstanceType: "t3.large", UseDebianAMI: true, RootSizeGB: 30, Bucket: true, BucketName: "web-logs-123"})
	if err != nil {
		t.Fatal(err)
	}
	est := EstimateIaC(aws, IaCOptions{Policies: dd.Policies}, prices, st)
	var inst *IaCItem
	for i := range est.Items {
		if est.Items[i].Type == "aws_instance" {
			inst = &est.Items[i]
		}
	}
	if inst == nil || inst.Monthly == nil || inst.Region != "us-east-1" || inst.Env != "dev" || inst.Tags["Owner"] != "web" {
		t.Fatalf("aws_instance do gerador: %+v\nnotas: %v\nnão suportados: %v", inst, est.Notes, est.Unsupported)
	}
	want := round2(0.0832*730 + 30*0.08)
	if *inst.Monthly != want {
		t.Fatalf("custo %v, esperado %v (%+v)", *inst.Monthly, want, inst.Components)
	}
	if len(est.Unsupported) != 0 {
		t.Fatalf("tipos sem modelo no código gerado: %v", est.Unsupported)
	}
	oci, err := gen.Tofu(gen.TofuSpec{Provider: "oci", Name: "erp", Region: "sa-saopaulo-1", Tags: "Project=erp", Network: true, NetCIDR: "10.1.0.0/16", SubnetCIDR: "10.1.1.0/24", SSHCIDR: "10.0.0.0/8", Compute: true,
		Shape: "VM.Standard.E5.Flex", OCPUs: 2, MemoryGB: 16, RootSizeGB: 60, BlockVolume: true, VolumeSizeGB: 200, VPUs: 10})
	if err != nil {
		t.Fatal(err)
	}
	est = EstimateIaC(oci, IaCOptions{}, prices, st)
	if len(est.Unpriced) != 0 || len(est.Unsupported) != 0 || est.Total <= 0 {
		t.Fatalf("OCI gerado: total=%v sem preço=%v não suportados=%v itens=%+v", est.Total, est.Unpriced, est.Unsupported, est.Items)
	}
	for _, it := range est.Items {
		if it.Type == "oci_core_instance" && it.Tags["Project"] != "erp" {
			t.Fatalf("tags OCI: %+v", it.Tags)
		}
	}
}
