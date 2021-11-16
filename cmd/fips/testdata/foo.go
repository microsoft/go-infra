package foo

import "github.com/microsoft/go-infra/cmd/fips/internal/boring"

func F1() {
	if boringEnabled() {
		boring.A()
	}
	noBoring()
}

func F2() {
	if boring.Enabled() {
		boring.A()
		boring.B()
		boring.C()
	}
	noBoring()
}

func F3() {
	if boring.Enabled() {
		noBoring()
	}
	boring.A()
}

func boringEnabled() bool { return boring.Enabled() }
func noBoring()           {}
