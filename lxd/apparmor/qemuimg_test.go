package apparmor

import (
	"bytes"
	"fmt"
	"testing"
)

func TestHandleWriter(t *testing.T) {
	status := []int64{}
	var buffer bytes.Buffer
	out := &nullWriteCloser{handleWriter(&buffer, func(percent int64, _ int64) {
		status = append(status, percent)
	})}

	for i := range 101 {
		for j := range 100 {
			n, err := fmt.Fprintf(out, "\t    (%02d.%02d/100%s)\r", i, j, "%")
			if err != nil {
				t.Fatal(err, n)
			}

			if i == 100 {
				break
			}
		}
	}

	if len(status) != 100 {
		t.Fatal(status)
	}

	for i := int64(1); i < 101; i++ {
		if status[i-1] != i {
			t.Fatal(status[i], i)
		}
	}

	// Do not check output carefully.
	output := buffer.String()
	if len(output) == 0 {
		t.Fatal(output)
	}
}
