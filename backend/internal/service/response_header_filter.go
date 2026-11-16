package service

import (
	"github.com/DeltrivX/Auto2API/internal/config"
	"github.com/DeltrivX/Auto2API/internal/util/responseheaders"
)

func compileResponseHeaderFilter(cfg *config.Config) *responseheaders.CompiledHeaderFilter {
	if cfg == nil {
		return nil
	}
	return responseheaders.CompileHeaderFilter(cfg.Security.ResponseHeaders)
}
