package limits

// Image pricing for vision input.
//
// Providers charge vision input by **tiles**, not by bytes: a 4096×4096 PNG and
// a 512×512 JPEG cost different amounts, and a bigger file does not cost more
// than the same pixels stored losslessly. The admission layer therefore needs
// the pixel geometry, and the pricing below is the single place that turns it
// into tokens.

const (
	// ImageTileSize is the tile edge used for pricing (pixels).
	ImageTileSize = 512

	// ImageBaseTokens covers the part of the charge that is independent of
	// tile count (provider-side resizing and metadata).
	ImageBaseTokens = 85

	// ImageTileTokens is the charge per tile.
	ImageTileTokens = 170

	// ImageUnknownSizeTokens is the fallback for an image whose geometry was
	// not carried along. It approximates one 1024×1024 image (4 tiles);
	// overestimating is deliberate — the alternative is under-counting quota.
	ImageUnknownSizeTokens = ImageBaseTokens + 4*ImageTileTokens
)

// ImageTokens estimates the input tokens one image costs.
//
// Unknown geometry (either dimension <= 0) falls back to a fixed estimate
// instead of zero: an image that costs quota must never be admitted as free.
func ImageTokens(width, height int) int {
	if width <= 0 || height <= 0 {
		return ImageUnknownSizeTokens
	}
	tilesX := (width + ImageTileSize - 1) / ImageTileSize
	tilesY := (height + ImageTileSize - 1) / ImageTileSize
	return ImageBaseTokens + tilesX*tilesY*ImageTileTokens
}
