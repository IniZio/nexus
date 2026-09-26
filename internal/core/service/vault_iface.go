package service

import "github.com/IniZio/nexus/internal/core/vault"

func (s *Service) WithVault(v vault.Vault) *Service {
	s.vault = v
	return s
}
