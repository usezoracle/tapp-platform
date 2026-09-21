/** Common retail merchant category codes. Anything else goes in as free text. */
export const MCC_OPTIONS: { code: string; label: string }[] = [
  { code: "5411", label: "Grocery stores and supermarkets" },
  { code: "5499", label: "Convenience and food stores" },
  { code: "5441", label: "Bakeries and confectionery" },
  { code: "5812", label: "Restaurants" },
  { code: "5814", label: "Fast food" },
  { code: "5813", label: "Bars and lounges" },
  { code: "5921", label: "Liquor stores" },
  { code: "5912", label: "Pharmacies" },
  { code: "5977", label: "Cosmetics" },
  { code: "7230", label: "Barber and beauty salons" },
  { code: "5651", label: "Clothing" },
  { code: "5661", label: "Shoe stores" },
  { code: "5311", label: "Department stores" },
  { code: "5331", label: "Variety stores" },
  { code: "5732", label: "Electronics" },
  { code: "5942", label: "Book stores" },
  { code: "5251", label: "Hardware" },
  { code: "5712", label: "Furniture and home furnishings" },
  { code: "5541", label: "Fuel stations" },
  { code: "7011", label: "Hotels and lodging" },
  { code: "5999", label: "Other retail" },
];

export function mccLabel(code: string): string {
  const hit = MCC_OPTIONS.find((o) => o.code === code);
  return hit ? `${hit.code} · ${hit.label}` : code;
}
