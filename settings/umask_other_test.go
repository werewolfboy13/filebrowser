//go:build !unix

package settings

func syscallUmask(int) int { return 0 }
