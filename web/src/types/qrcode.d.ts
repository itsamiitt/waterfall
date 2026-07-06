// Hand-authored ambient types for the `qrcode` package (ADR-0016 allowlisted; the DT types
// package is not, so we declare the one call the MFA enrollment flow uses).
declare module "qrcode" {
  export interface QRCodeToDataURLOptions {
    margin?: number;
    width?: number;
    errorCorrectionLevel?: "L" | "M" | "Q" | "H";
    color?: { dark?: string; light?: string };
  }
  export function toDataURL(text: string, options?: QRCodeToDataURLOptions): Promise<string>;
}
