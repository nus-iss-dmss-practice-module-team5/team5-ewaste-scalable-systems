package service

import (
	"fmt"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestPrintSeedHashes(t *testing.T) {
	for i := 0; i < 9; i++ {
		hash, err := bcrypt.GenerateFromPassword([]byte("TestPassword123!"), bcrypt.DefaultCost)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println(string(hash))
	}
}
