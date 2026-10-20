package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_returnsDefaults_whenFileMissing(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ResyncInterval != 60*time.Second || cfg.LLDPD.PollInterval != 3*time.Second ||
		cfg.Routing.Protocol != 205 || cfg.Routing.TableBase != 200 || !cfg.IPv6.Disable ||
		cfg.OrgTLV.OUI != [3]byte{0x00, 0x0e, 0xab} || cfg.OrgTLV.Subtype != 1 ||
		len(cfg.Interfaces.PCIIDs) != 1 || cfg.Interfaces.PCIIDs[0] != (PCIID{0x17db, 0x0501}) {
		t.Errorf("defaults differ from spec §6.13: %+v", cfg)
	}
	if !cfg.Interfaces.NamePattern.MatchString("hsn0") || cfg.Interfaces.NamePattern.MatchString("eth0") {
		t.Errorf("default name pattern %q", cfg.Interfaces.NamePattern)
	}
}

func TestLoad_overridesOnlyGivenKeys_whenFilePartial(t *testing.T) {
	path := writeConfig(t, "resync_interval = \"30s\"\n[routing]\nprotocol = 210\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ResyncInterval != 30*time.Second || cfg.Routing.Protocol != 210 {
		t.Errorf("overrides not applied: %+v", cfg)
	}
	if cfg.Routing.TableBase != 200 || !cfg.Routing.MoveLocalRule {
		t.Errorf("defaults lost for keys absent from the file: %+v", cfg.Routing)
	}
}

func TestLoad_rejectsUnknownKey(t *testing.T) {
	_, err := Load(writeConfig(t, "[routing]\nprotocl = 210\n")) //nolint:misspell // deliberate typo: an unknown key
	if !errors.Is(err, ErrUnknownKeys) {
		t.Fatalf("err = %v, want ErrUnknownKeys", err)
	}
}

func TestLoad_rejectsInvalidValues(t *testing.T) {
	cases := map[string]string{
		"bad duration":       "resync_interval = \"soon\"\n",
		"zero duration":      "[lldpd]\npoll_interval = \"0s\"\n",
		"bad regex":          "[interfaces]\nname_pattern = '(['\n",
		"regex w/o group":    "[interfaces]\nname_pattern = '^hsn[0-9]+$'\n",
		"bad PCI ID":         "[interfaces]\npci_ids = [\"17db\"]\n",
		"bad OUI":            "[org_tlv]\noui = \"00:0e\"\n",
		"reserved protocol":  "[routing]\nprotocol = 2\n",
		"protocol too large": "[routing]\nprotocol = 300\n",
		"subtype too large":  "[org_tlv]\nsubtype = 256\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, content)); !errors.Is(err, ErrInvalid) {
				t.Errorf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestLoad_rejectsTypeMismatch(t *testing.T) {
	if _, err := Load(writeConfig(t, "[routing]\nenabled = \"yes\"\n")); err == nil {
		t.Fatal("want error for string in bool field")
	}
}

func TestParsePCIID_acceptsSysfsAndTalosForms(t *testing.T) {
	for _, s := range []string{"17db:0501", "17DB:0501", "0x17db:0x0501"} {
		id, err := ParsePCIID(s)
		if err != nil || id != (PCIID{0x17db, 0x0501}) {
			t.Errorf("ParsePCIID(%q) = %v, %v", s, id, err)
		}
	}
}
