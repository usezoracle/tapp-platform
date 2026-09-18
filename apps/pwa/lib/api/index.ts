/**
 * The API client.
 *
 * One module per resource, one shared transport. Import from `@/lib/api` and
 * take what you need; the split is so that adding a resource does not mean
 * editing a file everything else already imports.
 */

export * from "./http";
export * from "./money";

export * from "./activity";
export * from "./agents";
export * from "./balances";
export * from "./cards";
export * from "./cash";
export * from "./checkout";
export * from "./convert";
export * from "./deposits";
export * from "./holdings";
export * from "./kyc";
export * from "./withdrawals";
