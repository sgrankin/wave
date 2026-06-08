package conv

import (
	"fmt"
	"testing"

	"github.com/sgrankin/wave/internal/op"
)

func TestClearStyledResidueDetail(t *testing.T) {
	content := styledContent(t)
	clear := clearOp(content)
	res, err := op.Apply(content, clear)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	fmt.Println("RESULT components:")
	for i, c := range res.Components() {
		fmt.Printf("  [%d] %T %+v\n", i, c, c)
	}
	fmt.Printf("res len=%d initial len=%d\n", res.DocumentLength(), InitialBlipContent().DocumentLength())

	// Does the residual-bearing result read as a valid blip body? Try doc.Root.
	if _, err := readBodyRoot(res); err != nil {
		fmt.Printf("readBodyRoot ERROR: %v\n", err)
	} else {
		fmt.Println("readBodyRoot OK")
	}
}

// readBodyRoot is a tiny probe: does the cleared content parse as <body>?
func readBodyRoot(content op.DocOp) (string, error) {
	for _, c := range content.Components() {
		if es, ok := c.(op.ElementStart); ok {
			return es.Type, nil
		}
	}
	return "", fmt.Errorf("no element start")
}
