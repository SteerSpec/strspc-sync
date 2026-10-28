package github

import "testing"

func TestAPIRepoVisibility(t *testing.T) {
	for _, tc := range []struct {
		name string
		repo apiRepo
		want string
	}{
		{"visibility field wins", apiRepo{Visibility: "internal", Private: true}, "internal"},
		{"private flag", apiRepo{Private: true}, "private"},
		{"default public", apiRepo{}, "public"},
	} {
		if got := tc.repo.toDomain().Visibility; got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
