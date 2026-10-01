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

export const seriesColors: Record<"light" | "dark", SeriesPalette> = {
  light: { delivered: "#2a78d6", failed: "#eb6834", grid: "#e4e4e7", axis: "#71717a" },
  dark: { delivered: "#3987e5", failed: "#d95926", grid: "#27272a", axis: "#a1a1aa" },
};
