package intrinsic

import lmfrt "zoa/lmfrt"

func NewRegistry() *lmfrt.Registry {
	r := lmfrt.NewRegistry()
	r.MustRegister(IntrinsicModifyCodebase())
	return r
}
