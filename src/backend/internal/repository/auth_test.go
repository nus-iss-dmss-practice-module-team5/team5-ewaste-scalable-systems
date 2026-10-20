package repository

import (
	"errors"
	"testing"
)

func TestGormAuthRepositoryImplementsAuthRepository(t *testing.T) {
	var _ AuthRepository = (*GormAuthRepository)(nil)
}

func TestRepositorySentinelErrorsAreDistinct(t *testing.T) {
	if errors.Is(ErrNotFound, ErrRotationRejected) || errors.Is(ErrRotationRejected, ErrNotFound) {
		t.Fatal("repository sentinel errors must remain distinguishable")
	}
}
