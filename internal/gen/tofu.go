package gen

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

// TofuSpec descreve a infraestrutura a gerar para OpenTofu (providers AWS e OCI).
type TofuSpec struct {
	Provider        string `json:"provider"` // aws | oci
	Name            string `json:"name"`     // prefixo dos recursos
	Region          string `json:"region"`
	ProviderVersion string `json:"provider_version"`
	Tags            string `json:"tags"` // linhas chave=valor

	// Rede (VPC / VCN)
	Network    bool   `json:"network"`
	NetCIDR    string `json:"net_cidr"`
	SubnetCIDR string `json:"subnet_cidr"`
	AZ         string `json:"az"`
	SSHCIDR    string `json:"ssh_cidr"`

	// Compute (EC2 / OCI Compute)
	Compute      bool   `json:"compute"`
	InstanceType string `json:"instance_type"` // AWS
	AMI          string `json:"ami"`           // AWS (vazio + UseDebianAMI)
	UseDebianAMI bool   `json:"use_debian_ami"`
	KeyName      string `json:"key_name"` // AWS key pair
	RootSizeGB   int    `json:"root_size_gb"`
	Shape        string `json:"shape"` // OCI
	OCPUs        int    `json:"ocpus"`
	MemoryGB     int    `json:"memory_gb"`

	// Armazenamento
	Bucket       bool   `json:"bucket"` // AWS S3
	BucketName   string `json:"bucket_name"`
	Versioning   bool   `json:"versioning"`
	BlockVolume  bool   `json:"block_volume"` // OCI
	VolumeSizeGB int    `json:"volume_size_gb"`
	VPUs         int    `json:"vpus"`
}

var tfNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)

// hcl quota uma string HCL (escapa interpolação ${ e diretivas %{).
func hcl(s string) string {
	q := DQ(s)
	q = strings.ReplaceAll(q, "${", "$${")
	return strings.ReplaceAll(q, "%{", "%%{")
}

func checkCIDR(label, c string) error {
	if _, err := netip.ParsePrefix(c); err != nil {
		return fmt.Errorf("%s inválido: %q", label, c)
	}
	return nil
}

// Tofu gera os arquivos .tf do projeto.
func Tofu(s TofuSpec) (map[string]string, error) {
	if !tfNameRe.MatchString(s.Name) {
		return nil, fmt.Errorf("nome deve ter 2–31 caracteres: minúsculas, números e hífen")
	}
	if s.Region == "" {
		return nil, fmt.Errorf("informe a região")
	}
	if s.Network {
		for _, c := range [][2]string{{"CIDR da rede", s.NetCIDR}, {"CIDR da subnet", s.SubnetCIDR}, {"CIDR liberado para SSH", s.SSHCIDR}} {
			if err := checkCIDR(c[0], c[1]); err != nil {
				return nil, err
			}
		}
		if s.SSHCIDR == "0.0.0.0/0" {
			return nil, fmt.Errorf("SSH aberto para 0.0.0.0/0 não é permitido: informe o seu IP/CIDR administrativo")
		}
	}
	if s.Compute && !s.Network {
		return nil, fmt.Errorf("a instância precisa da rede: marque também VPC/VCN")
	}
	switch s.Provider {
	case "aws":
		return tofuAWS(s)
	case "oci":
		return tofuOCI(s)
	}
	return nil, fmt.Errorf("provider não suportado: %q (use aws ou oci)", s.Provider)
}

func tagsBlock(s TofuSpec, indent string) string {
	var b strings.Builder
	b.WriteString(indent + "ManagedBy = \"opentofu\"\n")
	b.WriteString(indent + "Project   = " + hcl(s.Name) + "\n")
	for _, kv := range KVLines(s.Tags) {
		if IdentRe.MatchString(kv[0]) {
			b.WriteString(fmt.Sprintf("%s%s = %s\n", indent, kv[0], hcl(kv[1])))
		}
	}
	return b.String()
}

func tofuAWS(s TofuSpec) (map[string]string, error) {
	ver := def(s.ProviderVersion, "~> 6.0")
	f := map[string]string{}
	f["versions.tf"] = fmt.Sprintf(`terraform {
  required_version = ">= 1.8.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = %s
    }
  }

  # Recomendado: state remoto com lock e criptografia (S3 + use_lockfile) e a
  # criptografia de state nativa do OpenTofu. Veja:
  # https://opentofu.org/docs/language/settings/backends/s3/
  # https://opentofu.org/docs/language/state/encryption/
}
`, hcl(ver))
	f["providers.tf"] = fmt.Sprintf(`# Credenciais: NÃO escreva chaves aqui. O doomctl injeta AWS_ACCESS_KEY_ID /
# AWS_SECRET_ACCESS_KEY (ou AWS_PROFILE) a partir das credenciais criptografadas do projeto.
provider "aws" {
  region = var.region

  default_tags {
    tags = {
%s    }
  }
}
`, tagsBlock(s, "      "))

	var v, m, o strings.Builder
	v.WriteString(fmt.Sprintf("variable \"region\" {\n  type    = string\n  default = %s\n}\n\nvariable \"name\" {\n  type    = string\n  default = %s\n}\n", hcl(s.Region), hcl(s.Name)))

	if s.Network {
		az := s.AZ
		if az == "" {
			az = s.Region + "a"
		}
		v.WriteString(fmt.Sprintf(`
variable "vpc_cidr" {
  type    = string
  default = %s
}

variable "subnet_cidr" {
  type    = string
  default = %s
}

variable "availability_zone" {
  type    = string
  default = %s
}

variable "ssh_allowed_cidr" {
  description = "CIDR administrativo com acesso SSH (nunca 0.0.0.0/0)"
  type        = string
  default     = %s
}
`, hcl(s.NetCIDR), hcl(s.SubnetCIDR), hcl(az), hcl(s.SSHCIDR)))
		m.WriteString(`# ---------- Rede ----------
resource "aws_vpc" "main" {
  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = "${var.name}-vpc" }
}

resource "aws_internet_gateway" "main" {
  vpc_id = aws_vpc.main.id
  tags   = { Name = "${var.name}-igw" }
}

resource "aws_subnet" "public" {
  vpc_id                  = aws_vpc.main.id
  cidr_block              = var.subnet_cidr
  availability_zone       = var.availability_zone
  map_public_ip_on_launch = true
  tags                    = { Name = "${var.name}-public" }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.main.id
  }

  tags = { Name = "${var.name}-public-rt" }
}

resource "aws_route_table_association" "public" {
  subnet_id      = aws_subnet.public.id
  route_table_id = aws_route_table.public.id
}

resource "aws_security_group" "ssh" {
  name        = "${var.name}-ssh"
  description = "SSH somente do CIDR administrativo"
  vpc_id      = aws_vpc.main.id
}

resource "aws_vpc_security_group_ingress_rule" "ssh" {
  security_group_id = aws_security_group.ssh.id
  cidr_ipv4         = var.ssh_allowed_cidr
  from_port         = 22
  to_port           = 22
  ip_protocol       = "tcp"
}

resource "aws_vpc_security_group_egress_rule" "all" {
  security_group_id = aws_security_group.ssh.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}
`)
		o.WriteString("output \"vpc_id\" {\n  value = aws_vpc.main.id\n}\n\noutput \"subnet_id\" {\n  value = aws_subnet.public.id\n}\n")
	}

	if s.Compute {
		it := def(s.InstanceType, "t3.micro")
		root := s.RootSizeGB
		if root <= 0 {
			root = 20
		}
		v.WriteString(fmt.Sprintf("\nvariable \"instance_type\" {\n  type    = string\n  default = %s\n}\n", hcl(it)))
		amiExpr := "var.ami_id"
		if s.UseDebianAMI && s.AMI == "" {
			amiExpr = "data.aws_ami.debian.id"
			m.WriteString(`
# Debian 13 (trixie) oficial — conta da Debian na AWS. Confira em https://wiki.debian.org/Cloud/AmazonEC2Image
data "aws_ami" "debian" {
  most_recent = true
  owners      = ["136693071363"]

  filter {
    name   = "name"
    values = ["debian-13-amd64-*"]
  }

  filter {
    name   = "architecture"
    values = ["x86_64"]
  }
}
`)
		} else {
			if s.AMI == "" {
				return nil, fmt.Errorf("informe o AMI ID ou marque 'usar AMI oficial Debian 13'")
			}
			v.WriteString(fmt.Sprintf("\nvariable \"ami_id\" {\n  type    = string\n  default = %s\n}\n", hcl(s.AMI)))
		}
		keyLine := ""
		if s.KeyName != "" {
			v.WriteString(fmt.Sprintf("\nvariable \"key_name\" {\n  type    = string\n  default = %s\n}\n", hcl(s.KeyName)))
			keyLine = "  key_name               = var.key_name\n"
		}
		m.WriteString(fmt.Sprintf(`
# ---------- EC2 ----------
resource "aws_instance" "main" {
  ami                    = %s
  instance_type          = var.instance_type
  subnet_id              = aws_subnet.public.id
  vpc_security_group_ids = [aws_security_group.ssh.id]
%s
  # IMDSv2 obrigatório
  metadata_options {
    http_endpoint = "enabled"
    http_tokens   = "required"
  }

  root_block_device {
    volume_size = %d
    volume_type = "gp3"
    encrypted   = true
  }

  tags = { Name = "${var.name}-ec2" }
}
`, amiExpr, keyLine, root))
		o.WriteString("\noutput \"instance_id\" {\n  value = aws_instance.main.id\n}\n\noutput \"instance_public_ip\" {\n  value = aws_instance.main.public_ip\n}\n")
	}

	if s.Bucket {
		bn := s.BucketName
		if bn == "" {
			return nil, fmt.Errorf("informe o nome do bucket (globalmente único)")
		}
		if !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`).MatchString(bn) {
			return nil, fmt.Errorf("nome de bucket S3 inválido")
		}
		v.WriteString(fmt.Sprintf("\nvariable \"bucket_name\" {\n  type    = string\n  default = %s\n}\n", hcl(bn)))
		m.WriteString(`
# ---------- S3 ----------
resource "aws_s3_bucket" "main" {
  bucket = var.bucket_name
}

resource "aws_s3_bucket_public_access_block" "main" {
  bucket                  = aws_s3_bucket.main.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "main" {
  bucket = aws_s3_bucket.main.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}
`)
		if s.Versioning {
			m.WriteString(`
resource "aws_s3_bucket_versioning" "main" {
  bucket = aws_s3_bucket.main.id

  versioning_configuration {
    status = "Enabled"
  }
}
`)
		}
		o.WriteString("\noutput \"bucket\" {\n  value = aws_s3_bucket.main.bucket\n}\n")
	}
	if m.Len() == 0 {
		return nil, fmt.Errorf("selecione ao menos um recurso")
	}
	f["variables.tf"] = v.String()
	f["main.tf"] = strings.TrimLeft(m.String(), "\n")
	f["outputs.tf"] = strings.TrimLeft(o.String(), "\n")
	return f, nil
}

func tofuOCI(s TofuSpec) (map[string]string, error) {
	ver := def(s.ProviderVersion, "~> 9.0")
	f := map[string]string{}
	f["versions.tf"] = fmt.Sprintf(`terraform {
  required_version = ">= 1.8.0"

  required_providers {
    oci = {
      source  = "oracle/oci"
      version = %s
    }
  }

  # Recomendado: state remoto (OCI Object Storage via backend S3-compatível ou "oci")
  # e criptografia de state do OpenTofu: https://opentofu.org/docs/language/state/encryption/
}
`, hcl(ver))
	f["providers.tf"] = `# Autenticação por API key. Os valores vêm das credenciais criptografadas do
# projeto (injetadas como TF_VAR_*). Nunca versione a chave privada.
provider "oci" {
  tenancy_ocid = var.tenancy_ocid
  user_ocid    = var.user_ocid
  fingerprint  = var.fingerprint
  private_key  = var.private_key
  region       = var.region
}
`
	var v, m, o strings.Builder
	v.WriteString(fmt.Sprintf(`variable "tenancy_ocid" {
  type = string
}

variable "user_ocid" {
  type = string
}

variable "fingerprint" {
  type = string
}

variable "private_key" {
  type      = string
  sensitive = true
}

variable "compartment_ocid" {
  type = string
}

variable "region" {
  type    = string
  default = %s
}

variable "name" {
  type    = string
  default = %s
}

variable "freeform_tags" {
  type = map(string)
  default = {
%s  }
}
`, hcl(s.Region), hcl(s.Name), tagsBlock(s, "    ")))

	if s.Network || s.Compute || s.BlockVolume {
		m.WriteString(`data "oci_identity_availability_domains" "ads" {
  compartment_id = var.tenancy_ocid
}

locals {
  ad = data.oci_identity_availability_domains.ads.availability_domains[0].name
}
`)
	}
	if s.Network {
		v.WriteString(fmt.Sprintf(`
variable "vcn_cidr" {
  type    = string
  default = %s
}

variable "subnet_cidr" {
  type    = string
  default = %s
}

variable "ssh_allowed_cidr" {
  description = "CIDR administrativo com acesso SSH (nunca 0.0.0.0/0)"
  type        = string
  default     = %s
}
`, hcl(s.NetCIDR), hcl(s.SubnetCIDR), hcl(s.SSHCIDR)))
		m.WriteString(`
# ---------- Rede (VCN) ----------
resource "oci_core_vcn" "main" {
  compartment_id = var.compartment_ocid
  cidr_blocks    = [var.vcn_cidr]
  display_name   = "${var.name}-vcn"
  freeform_tags  = var.freeform_tags
}

resource "oci_core_internet_gateway" "main" {
  compartment_id = var.compartment_ocid
  vcn_id         = oci_core_vcn.main.id
  display_name   = "${var.name}-igw"
  enabled        = true
}

resource "oci_core_route_table" "public" {
  compartment_id = var.compartment_ocid
  vcn_id         = oci_core_vcn.main.id
  display_name   = "${var.name}-public-rt"

  route_rules {
    destination       = "0.0.0.0/0"
    destination_type  = "CIDR_BLOCK"
    network_entity_id = oci_core_internet_gateway.main.id
  }
}

resource "oci_core_security_list" "ssh" {
  compartment_id = var.compartment_ocid
  vcn_id         = oci_core_vcn.main.id
  display_name   = "${var.name}-ssh"

  egress_security_rules {
    destination = "0.0.0.0/0"
    protocol    = "all"
  }

  ingress_security_rules {
    protocol = "6" # TCP
    source   = var.ssh_allowed_cidr

    tcp_options {
      min = 22
      max = 22
    }
  }
}

resource "oci_core_subnet" "public" {
  compartment_id             = var.compartment_ocid
  vcn_id                     = oci_core_vcn.main.id
  cidr_block                 = var.subnet_cidr
  display_name               = "${var.name}-public"
  route_table_id             = oci_core_route_table.public.id
  security_list_ids          = [oci_core_security_list.ssh.id]
  prohibit_public_ip_on_vnic = false
}
`)
		o.WriteString("output \"vcn_id\" {\n  value = oci_core_vcn.main.id\n}\n\noutput \"subnet_id\" {\n  value = oci_core_subnet.public.id\n}\n")
	}
	if s.Compute {
		shape := def(s.Shape, "VM.Standard.E5.Flex")
		ocpus, mem := s.OCPUs, s.MemoryGB
		if ocpus <= 0 {
			ocpus = 1
		}
		if mem <= 0 {
			mem = 8
		}
		boot := s.RootSizeGB
		if boot < 50 {
			boot = 50
		}
		v.WriteString(fmt.Sprintf(`
variable "shape" {
  type    = string
  default = %s
}

variable "image_ocid" {
  description = "OCID da imagem (Console > Compute > Images) — específico por região"
  type        = string
}

variable "ssh_public_key" {
  type = string
}
`, hcl(shape)))
		shapeCfg := ""
		if strings.HasSuffix(shape, ".Flex") {
			shapeCfg = fmt.Sprintf("\n  shape_config {\n    ocpus         = %d\n    memory_in_gbs = %d\n  }\n", ocpus, mem)
		}
		m.WriteString(fmt.Sprintf(`
# ---------- Compute ----------
resource "oci_core_instance" "main" {
  compartment_id      = var.compartment_ocid
  availability_domain = local.ad
  display_name        = "${var.name}-vm"
  shape               = var.shape
  freeform_tags       = var.freeform_tags
%s
  source_details {
    source_type             = "image"
    source_id               = var.image_ocid
    boot_volume_size_in_gbs = %d
  }

  create_vnic_details {
    subnet_id        = oci_core_subnet.public.id
    assign_public_ip = true
  }

  metadata = {
    ssh_authorized_keys = var.ssh_public_key
  }
}
`, shapeCfg, boot))
		o.WriteString("\noutput \"instance_id\" {\n  value = oci_core_instance.main.id\n}\n\noutput \"instance_public_ip\" {\n  value = oci_core_instance.main.public_ip\n}\n")
	}
	if s.BlockVolume {
		size := s.VolumeSizeGB
		if size < 50 {
			size = 50
		}
		vpus := s.VPUs
		if vpus <= 0 {
			vpus = 10
		}
		m.WriteString(fmt.Sprintf(`
# ---------- Block Volume ----------
# Após anexar: particione/formate no SO. Ao expandir o volume, expanda também a partição e o FS.
resource "oci_core_volume" "data" {
  compartment_id      = var.compartment_ocid
  availability_domain = local.ad
  display_name        = "${var.name}-data"
  size_in_gbs         = %d
  vpus_per_gb         = %d
  freeform_tags       = var.freeform_tags
}
`, size, vpus))
		if s.Compute {
			m.WriteString(`
resource "oci_core_volume_attachment" "data" {
  attachment_type = "paravirtualized"
  instance_id     = oci_core_instance.main.id
  volume_id       = oci_core_volume.data.id
}
`)
		}
		o.WriteString("\noutput \"volume_id\" {\n  value = oci_core_volume.data.id\n}\n")
	}
	if !s.Network && !s.Compute && !s.BlockVolume {
		return nil, fmt.Errorf("selecione ao menos um recurso")
	}
	f["variables.tf"] = v.String()
	f["main.tf"] = m.String()
	f["outputs.tf"] = strings.TrimLeft(o.String(), "\n")
	return f, nil
}
