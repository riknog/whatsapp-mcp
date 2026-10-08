package wa

import (
	"context"
	"errors"
	"fmt"

	"go.mau.fi/whatsmeow"

	"github.com/riknog/whatsapp-mcp/internal/privacy"
)

// Media holds what is needed to download and decrypt one audio or image file.
type Media struct {
	Kind          string // "audio" | "image"
	DirectPath    string
	MediaKey      []byte
	FileSHA256    []byte
	FileEncSHA256 []byte
}

// ErrMediaGone means WhatsApp no longer serves the file (expired or removed).
// Retrying does not help.
var ErrMediaGone = errors.New("wa: mídia não está mais disponível no WhatsApp")

// ErrMediaKind means the media kind is not one DownloadMedia handles.
var ErrMediaKind = errors.New("wa: tipo de mídia não suportado")

// DownloadMedia downloads and decrypts m.
func (r *Real) DownloadMedia(ctx context.Context, m Media) ([]byte, error) {
	var kind whatsmeow.MediaType
	switch m.Kind {
	case "audio":
		kind = whatsmeow.MediaAudio
	case "image":
		kind = whatsmeow.MediaImage
	default:
		return nil, ErrMediaKind
	}
	data, err := r.cl.DownloadMediaWithPath(ctx, m.DirectPath, m.FileEncSHA256, m.FileSHA256, m.MediaKey, kind, "", false)
	if err != nil {
		return nil, mediaError(err)
	}
	return data, nil
}

// mediaError keeps whatsmeow text, which can carry paths and hosts, out of the
// output, and sorts the cases the caller can act on.
func mediaError(err error) error {
	switch {
	case errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith403),
		errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404),
		errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410):
		return ErrMediaGone
	case errors.Is(err, whatsmeow.ErrNotConnected):
		return fmt.Errorf("%w: sem conexão no momento do download", ErrNetwork)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return fmt.Errorf("%w: download interrompido", ErrNetwork)
	}
	return fmt.Errorf("wa: download recusado: %s", privacy.RedactLog(err.Error()))
}
