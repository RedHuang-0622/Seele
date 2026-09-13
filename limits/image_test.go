package limits

import (
	"testing"

	"github.com/RedHuang-0622/Seele/types"
)

func TestImageTokensTileModel(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
		want          int
	}{
		{name: "单个 tile", width: 512, height: 512, want: ImageBaseTokens + ImageTileTokens},
		{name: "跨 tile 边界向上取整", width: 513, height: 512, want: ImageBaseTokens + 2*ImageTileTokens},
		{name: "1024 见方四个 tile", width: 1024, height: 1024, want: ImageBaseTokens + 4*ImageTileTokens},
		{name: "尺寸未知走兜底", width: 0, height: 0, want: ImageUnknownSizeTokens},
		{name: "只有宽度也走兜底", width: 800, height: 0, want: ImageUnknownSizeTokens},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ImageTokens(testCase.width, testCase.height); got != testCase.want {
				t.Fatalf("ImageTokens(%d,%d) = %d, want %d", testCase.width, testCase.height, got, testCase.want)
			}
		})
	}
}

func TestDefaultEstimatorCountsImagesPerTile(t *testing.T) {
	text := "看图"
	messages := []types.Message{{
		Role:    "user",
		Content: &text,
		Images: []types.ImagePart{{
			MimeType: "image/png",
			Data:     []byte{1},
			Width:    1024,
			Height:   1024,
		}},
	}}
	cost := DefaultEstimator(messages, nil)
	if cost.Images != 1 {
		t.Fatalf("Cost.Images = %d, want 1", cost.Images)
	}
	want := EstimateTextTokens(text) + ImageTokens(1024, 1024)
	if cost.InputTokens != want {
		t.Fatalf("InputTokens = %d, want %d", cost.InputTokens, want)
	}
	if cost.Requests != 1 {
		t.Fatalf("Requests = %d, want 1", cost.Requests)
	}
}

func TestDefaultEstimatorLeavesTextOnlyCostUnchanged(t *testing.T) {
	text := "只有文字"
	cost := DefaultEstimator([]types.Message{{Role: "user", Content: &text}}, nil)
	if cost.Images != 0 {
		t.Fatalf("Cost.Images = %d, want 0", cost.Images)
	}
	if want := EstimateTextTokens(text); cost.InputTokens != want {
		t.Fatalf("InputTokens = %d, want %d", cost.InputTokens, want)
	}
}

func TestCostWeightKeepsImagesOutOfTokens(t *testing.T) {
	cost := Cost{Requests: 1, InputTokens: 10, Images: 2}
	if got, want := cost.Tokens(), 10; got != want {
		t.Fatalf("Tokens = %d, want %d（图片只计权重，不进 token）", got, want)
	}
	if got, want := cost.Weight(DefaultImageWeight), 1+2*DefaultImageWeight; got != want {
		t.Fatalf("Weight = %v, want %v", got, want)
	}
}
