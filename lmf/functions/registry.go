package functions

import lmf "codexagentbase/lmf/runtime"

func NewRegistry() *lmf.Registry {
	r := lmf.NewRegistry()
	r.MustRegister(IntrinsicModifyCodebase())
	return r
}
