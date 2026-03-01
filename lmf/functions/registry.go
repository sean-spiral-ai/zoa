package functions

import lmf "zoa/lmf/runtime"

func NewRegistry() *lmf.Registry {
	r := lmf.NewRegistry()
	r.MustRegister(IntrinsicModifyCodebase())
	return r
}
