package domain

import "testing"

func TestClassifyGifCategoryUsesTitleThenFilename(t *testing.T) {
	tests := []struct {
		title, filename, want string
	}{
		{"Happy birthday", "cat.gif", GifCategoryCelebration},
		{"Untitled", "sleepy_cat.gif", GifCategoryAnimals},
		{"Untitled", "CatDance.gif", GifCategoryAnimals},
		{"Untitled", "HappyBirthday.gif", GifCategoryCelebration},
		{"Funny scene", "football.mp4", GifCategoryHumor},
		{"你好", "clip.gif", GifCategoryReaction},
		{"Catch the ball", "clip.gif", GifCategoryOther},
		{"Unknown", "clip.gif", GifCategoryOther},
	}
	for _, tt := range tests {
		if got := ClassifyGifCategory(tt.title, tt.filename); got != tt.want {
			t.Errorf("ClassifyGifCategory(%q, %q) = %q, want %q", tt.title, tt.filename, got, tt.want)
		}
	}
}

func TestGifCategoryMatchesQuery(t *testing.T) {
	if !GifCategoryMatchesQuery(GifCategoryAnimals, "猫") || !GifCategoryMatchesQuery(GifCategoryAnimals, "animal") || GifCategoryMatchesQuery(GifCategorySports, "猫") {
		t.Fatal("category query matching failed")
	}
}
