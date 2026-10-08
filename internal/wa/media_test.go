package wa

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
)

func TestDownloadMediaRejectsUnknownKindWithoutNetwork(t *testing.T) {
	r := openTest(t)
	if _, err := r.DownloadMedia(context.Background(), Media{Kind: "video", DirectPath: "/v/x"}); !errors.Is(err, ErrMediaKind) {
		t.Errorf("DownloadMedia video = %v, want ErrMediaKind", err)
	}
}

func TestMediaErrorSortsCases(t *testing.T) {
	for _, raw := range []error{whatsmeow.ErrMediaDownloadFailedWith403, whatsmeow.ErrMediaDownloadFailedWith404,
		whatsmeow.ErrMediaDownloadFailedWith410} {
		if err := mediaError(fmt.Errorf("x: %w", raw)); !errors.Is(err, ErrMediaGone) {
			t.Errorf("%v: %v, want ErrMediaGone", raw, err)
		}
	}
	for _, raw := range []error{whatsmeow.ErrNotConnected, context.DeadlineExceeded} {
		if err := mediaError(raw); !IsTransient(err) {
			t.Errorf("%v: %v, want transient", raw, err)
		}
	}
	msg := mediaError(errors.New("falha em 5511999998888@s.whatsapp.net")).Error()
	if strings.Contains(msg, "5511999998888") {
		t.Errorf("error text leaks the number: %s", msg)
	}
}

func TestFakeDownloadMedia(t *testing.T) {
	f := NewFake(nil)
	ctx := context.Background()
	f.SetMedia("/v/a", []byte("ogg"))
	if b, err := f.DownloadMedia(ctx, Media{Kind: "audio", DirectPath: "/v/a"}); err != nil || string(b) != "ogg" {
		t.Fatalf("DownloadMedia = %q, %v", b, err)
	}
	if _, err := f.DownloadMedia(ctx, Media{Kind: "image", DirectPath: "/v/none"}); !errors.Is(err, ErrMediaGone) {
		t.Errorf("unknown path = %v", err)
	}
	if _, err := f.DownloadMedia(ctx, Media{Kind: "video", DirectPath: "/v/a"}); !errors.Is(err, ErrMediaKind) {
		t.Errorf("video = %v", err)
	}
	f.SetMediaError(ErrMediaGone)
	if _, err := f.DownloadMedia(ctx, Media{Kind: "audio", DirectPath: "/v/a"}); !errors.Is(err, ErrMediaGone) {
		t.Errorf("scripted = %v", err)
	}
	f.SetMediaError(nil)
	f.SetConnected(false)
	if _, err := f.DownloadMedia(ctx, Media{Kind: "audio", DirectPath: "/v/a"}); !IsTransient(err) {
		t.Errorf("disconnected = %v", err)
	}
	if f.Downloads() != 5 {
		t.Errorf("Downloads = %d", f.Downloads())
	}
}
