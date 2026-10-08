package identity

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

var refShape = regexp.MustCompile(`^c_[a-z2-7]{10}$`)

func TestRefIsStableForKeyAndChangesWithKey(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	a, err := NewRefs(key)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewRefs(key)
	other, _ := NewRefs(bytes.Repeat([]byte{2}, 32))

	jid := "5511987654321@s.whatsapp.net"
	ra := a.Ref(jid)
	if !refShape.MatchString(ra) {
		t.Fatalf("ref %q não tem o formato c_ + 10 base32", ra)
	}
	if rb := b.Ref(jid); rb != ra {
		t.Fatalf("mesma chave deu refs diferentes: %q x %q", ra, rb)
	}
	if ro := other.Ref(jid); ro == ra {
		t.Fatalf("chave diferente deu a mesma ref %q", ro)
	}
	if rc := a.Ref("5511000000000@s.whatsapp.net"); rc == ra {
		t.Fatalf("JIDs diferentes deram a mesma ref")
	}
}

func TestRefCanonicalizesDeviceSuffixAndCase(t *testing.T) {
	r, _ := NewRefs(bytes.Repeat([]byte{7}, 32))
	base := r.Ref("5511987654321@s.whatsapp.net")
	if got := r.Ref("5511987654321:12@s.whatsapp.net"); got != base {
		t.Errorf("sufixo de device mudou a ref: %q x %q", got, base)
	}
	if got := r.Ref("5511987654321@S.WHATSAPP.NET"); got != base {
		t.Errorf("caixa mudou a ref: %q x %q", got, base)
	}
}

func TestNewRefsRejectsWrongKeySize(t *testing.T) {
	if _, err := NewRefs(make([]byte, 16)); err == nil {
		t.Fatal("chave de 16 bytes aceita")
	}
}

func TestLoadRefsCreatesKeyWith0600AndReusesIt(t *testing.T) {
	home := filepath.Join(t.TempDir(), "nested", "home")
	first, err := LoadRefs(home)
	if err != nil {
		t.Fatalf("LoadRefs: %v", err)
	}
	path := filepath.Join(home, KeyFileName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("ref.key não criado: %v", err)
	}
	if info.Size() != 32 {
		t.Errorf("tamanho do ref.key = %d, esperado 32", info.Size())
	}
	if mode := info.Mode().Perm(); mode != 0o600 && runtime.GOOS != "windows" {
		t.Errorf("permissão do ref.key = %v, esperado 0600", mode)
	}
	second, err := LoadRefs(home)
	if err != nil {
		t.Fatalf("segunda carga: %v", err)
	}
	jid := "120363000000000000@g.us"
	if first.Ref(jid) != second.Ref(jid) {
		t.Fatal("recarregar a chave mudou as refs")
	}
}

func TestLoadRefsRejectsWrongSizeKey(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, KeyFileName), []byte("curta"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRefs(home); err == nil {
		t.Fatal("ref.key com tamanho errado aceito")
	}
}

func TestLoadRefsRejectsWrongMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows não tem modos Unix")
	}
	home := t.TempDir()
	path := filepath.Join(home, KeyFileName)
	if err := os.WriteFile(path, bytes.Repeat([]byte{9}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadRefs(home)
	if err == nil {
		t.Fatal("ref.key com modo 0644 aceito")
	}
	if !strings.Contains(err.Error(), "0600") || !strings.Contains(err.Error(), "chmod") {
		t.Errorf("erro sem orientação clara: %v", err)
	}
	// The file is not changed silently.
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o644 {
		t.Errorf("modo foi alterado silenciosamente para %v", info.Mode().Perm())
	}
}

func TestLoadRefsWaitsForKeyBeingWritten(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, KeyFileName)
	// Another process has just created the file and has not written it yet.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{4}, 32)
	done := make(chan error, 1)
	go func() {
		time.Sleep(10 * time.Millisecond)
		_, werr := f.Write(key)
		cerr := f.Close()
		if werr != nil {
			done <- werr
			return
		}
		done <- cerr
	}()

	refs, err := LoadRefs(home)
	if werr := <-done; werr != nil {
		t.Fatal(werr)
	}
	if err != nil {
		t.Fatalf("LoadRefs não esperou a escrita: %v", err)
	}
	want, _ := NewRefs(key)
	if refs.Ref("5511@s.whatsapp.net") != want.Ref("5511@s.whatsapp.net") {
		t.Error("chave lida não é a escrita pelo outro processo")
	}
}

func TestLoadRefsGivesUpOnKeyThatStaysEmpty(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, KeyFileName)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRefs(home); err == nil {
		t.Fatal("ref.key vazio aceito")
	}
}
