package main

import (
	"fmt"
	"log"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/dcgm"
)

// dcgmi introspect --enable
// dcgmi introspect -s -H
func main() {
	cleanup, err := dcgm.Init(dcgm.Embedded)
	if err != nil {
		log.Panicln(err)
	}
	defer cleanup()

	st, err := dcgm.Introspect()
	if err != nil {
		log.Panicln(err)
	}

	fmt.Printf("Memory %2s %v KB\nCPU %5s %.2f %s\n", ":", st.Memory, ":", st.CPU, "%")
}
