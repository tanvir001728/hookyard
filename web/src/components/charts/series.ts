// Chart series colors, validated with the dataviz palette validator for
// lightness, chroma, color-vision-deficiency separation and surface contrast
// in both themes. Red/green failed the deuteranopia check, so delivered vs
// failed uses blue/orange, with labels carrying the meaning.
export interface SeriesPalette {
  delivered: string;
  failed: string;
  grid: string;
  axis: string;
}

// Latency percentiles are ordered, so they use one hue in three steps
// (validated as an ordinal ramp against each theme's card surface). The tail,
// p99, has the most contrast in both themes.
export const latencyColors: Record<"light" | "dark", { p50: string; p95: string; p99: string }> = {
  light: { p50: "#86b6ef", p95: "#2a78d6", p99: "#104281" },
  dark: { p50: "#184f95", p95: "#3987e5", p99: "#9ec5f4" },
};

export const seriesColors: Record<"light" | "dark", SeriesPalette> = {
  light: { delivered: "#2a78d6", failed: "#eb6834", grid: "#e4e4e7", axis: "#71717a" },
  dark: { delivered: "#3987e5", failed: "#d95926", grid: "#27272a", axis: "#a1a1aa" },
};
