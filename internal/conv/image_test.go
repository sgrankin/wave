package conv_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/conv"
	"github.com/sgrankin/wave/internal/op"
)

func TestInsertAndReadImage(t *testing.T) {
	body := bodyWithText(t, "pic") // <body><line/>pic</body>; offset 5 = end of "pic"
	imgOp, err := conv.InsertImage(body, "att123", 5)
	if err != nil {
		t.Fatal(err)
	}
	withImg, err := op.Apply(body, imgOp)
	if err != nil {
		t.Fatal(err)
	}
	imgs := conv.ReadImages(withImg)
	if len(imgs) != 1 || imgs[0].AttachmentID != "att123" || imgs[0].Offset != 5 {
		t.Errorf("images = %+v, want [{att123 5}]", imgs)
	}
}

func TestInsertImageRange(t *testing.T) {
	body := conv.InitialBlipContent()
	n := body.DocumentLength()
	if _, err := conv.InsertImage(body, "a", -1); err == nil {
		t.Error("offset -1 should error")
	}
	if _, err := conv.InsertImage(body, "a", n+1); err == nil {
		t.Error("offset > len should error")
	}
	end, err := conv.InsertImage(body, "a", n)
	if err != nil {
		t.Fatalf("offset == len should be allowed: %v", err)
	}
	out, err := op.Apply(body, end)
	if err != nil {
		t.Fatalf("apply at end: %v", err)
	}
	if imgs := conv.ReadImages(out); len(imgs) != 1 || imgs[0].Offset != n {
		t.Errorf("images after end-insert = %+v, want one at %d", imgs, n)
	}
}
