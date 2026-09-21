import { cn } from "@/lib/utils";

/**
 * A currency's mark, the way users-app draws it: the token's own SVG for
 * the ones we know (USDC, USDT, and the cNGN mark for naira), and a round
 * lettered badge for any other code. The SVGs carry their brand colours
 * and are the same in both themes; only the fallback badge reads tokens.
 *
 * Ported from users-app/constants/currencyIcons/cryptoCurrencyIcons.ts.
 */
const USDC = `<svg viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg"><path d="M12 24C18.6504 24 24 18.6504 24 12C24 5.3496 18.6504 0 12 0C5.3496 0 0 5.3496 0 12C0 18.6504 5.3496 24 12 24Z" fill="#2775CA"/><path d="M15.3 13.8996C15.3 12.15 14.25 11.55 12.15 11.2992C10.65 11.0988 10.35 10.6992 10.35 9.99961C10.35 9.30001 10.8504 8.85001 11.85 8.85001C12.75 8.85001 13.2504 9.15001 13.5 9.90001C13.5504 10.05 13.7004 10.1496 13.8504 10.1496H14.6508C14.8512 10.1496 15.0012 9.99961 15.0012 9.79921V9.74881C14.8008 8.64841 13.9008 7.79881 12.7512 7.69921V6.49921C12.7512 6.29881 12.6012 6.14881 12.3516 6.09961H11.6016C11.4012 6.09961 11.2512 6.24961 11.202 6.49921V7.64881C9.70196 7.84921 8.75156 8.84881 8.75156 10.0992C8.75156 11.7492 9.75116 12.3996 11.8512 12.6492C13.2516 12.8988 13.7016 13.1988 13.7016 13.9992C13.7016 14.7996 13.002 15.3492 12.0516 15.3492C10.752 15.3492 10.302 14.7996 10.152 14.0496C10.1016 13.8492 9.95156 13.7496 9.80156 13.7496H8.95196C8.75156 13.7496 8.60156 13.8996 8.60156 14.1V14.1504C8.80196 15.4008 9.60116 16.3008 11.2512 16.5504V17.7504C11.2512 17.9508 11.4012 18.1008 11.6508 18.15H12.4008C12.6012 18.15 12.7512 18 12.8004 17.7504V16.5504C14.2992 16.2996 15.3 15.2496 15.3 13.8996Z" fill="white"/><path d="M9.44984 19.1492C5.54984 17.7488 3.54944 13.3988 5.00024 9.5492C5.75024 7.4492 7.40024 5.8496 9.44984 5.0996C9.65024 5 9.74984 4.85 9.74984 4.5992V3.8996C9.74984 3.6992 9.65024 3.5492 9.44984 3.5C9.39944 3.5 9.29984 3.5 9.24944 3.5504C4.49984 5.0504 1.89944 10.1 3.39944 14.8508C4.29944 17.6504 6.44984 19.8008 9.24944 20.7008C9.44984 20.8004 9.64904 20.7008 9.69944 20.5004C9.74984 20.45 9.74984 20.4008 9.74984 20.3V19.6004C9.74984 19.4492 9.59984 19.25 9.44984 19.1492ZM14.749 3.5492C14.5486 3.4496 14.3494 3.5492 14.299 3.7496C14.2486 3.8 14.2486 3.8492 14.2486 3.95V4.6496C14.2486 4.85 14.3986 5.0492 14.5486 5.15C18.4486 6.5504 20.449 10.9004 18.9982 14.75C18.2482 16.85 16.5982 18.4496 14.5486 19.1996C14.3482 19.2992 14.2486 19.4492 14.2486 19.7V20.3996C14.2486 20.6 14.3482 20.75 14.5486 20.7992C14.599 20.7992 14.6986 20.7992 14.749 20.7488C19.4986 19.2488 22.099 14.1992 20.599 9.4484C19.699 6.5996 17.4994 4.4492 14.749 3.5492Z" fill="white"/></svg>`;

const USDT = `<svg viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg"><path fill-rule="evenodd" clip-rule="evenodd" d="M12 24C18.6274 24 24 18.6274 24 12C24 5.37258 18.6274 0 12 0C5.37258 0 0 5.37258 0 12C0 18.6274 5.37258 24 12 24Z" fill="#50AF95"/><path fill-rule="evenodd" clip-rule="evenodd" d="M13.5 11.7C13.425 11.7075 13.05 11.73 12.015 11.73C11.205 11.73 10.725 11.7075 10.5 11.7C7.5 11.55 5.25 11.025 5.25 10.395C5.25 9.765 7.5 9.24 10.5 9.09V11.16C10.725 11.175 11.22 11.205 12.0225 11.205C12.9825 11.205 13.425 11.1675 13.5 11.16V9.0975C16.5 9.2475 18.75 9.7725 18.75 10.395C18.75 11.025 16.5 11.55 13.5 11.6925V11.7ZM13.5 8.88V7.05H17.7V4.5H6.3V7.05H10.5V8.8725C7.0875 9.045 4.5 9.7275 4.5 10.5375C4.5 11.355 7.0875 12.03 10.5 12.21V18H13.5V12.2025C16.905 12.0225 19.485 11.3475 19.485 10.5375C19.485 9.7275 16.9125 9.0525 13.5 8.88V8.88Z" fill="white"/></svg>`;

/* The cNGN mark: the naira flag, green with a white band, in a disc. The
   source clips it with a circle; here the wrapper's round overflow does
   that, so no clipPath id is needed and two copies on one page cannot
   collide. */
const CNGN = `<svg viewBox="0 0 512 512" fill="none" xmlns="http://www.w3.org/2000/svg"><path fill="#6DA544" d="M0 0h512v512H0Z"/><path fill="#EEEEEE" d="M160 0h192v512H160Z"/></svg>`;

const MARKS: Record<string, string> = {
  USDC: USDC,
  USDT: USDT,
  CNGN: CNGN,
  NGN: CNGN,
};

export function CurrencyIcon({
  currency,
  size = 16,
  className,
}: {
  currency: string;
  /** Pixel diameter. 16 in a row, 14 in a chip. */
  size?: number;
  className?: string;
}) {
  const code = currency.trim().toUpperCase();
  const xml = MARKS[code];
  const box = { width: size, height: size };

  if (xml) {
    return (
      <span
        aria-hidden
        style={box}
        className={cn("inline-block shrink-0 overflow-hidden rounded-full [&>svg]:block [&>svg]:size-full", className)}
        dangerouslySetInnerHTML={{ __html: xml }}
      />
    );
  }

  return (
    <span
      aria-hidden
      style={{ ...box, fontSize: Math.max(7, Math.round(size * 0.42)) }}
      className={cn(
        "inline-grid shrink-0 place-items-center overflow-hidden rounded-full border border-line bg-sunken font-medium leading-none text-fg-muted",
        className,
      )}
    >
      {code.slice(0, 2)}
    </span>
  );
}
