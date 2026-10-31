import { format } from "@fixture/money";
import { rate } from "@fixture/money/rates";
import * as copy from "@money/format";
import { lazy } from "react";
import logo from "./logo.svg";
import type { Line } from "./types";

const Cart = lazy(() => import("./pages/cart"));
export const TITLE = "Shop";

export function App() {
  const l: Line = { cents: 100 };
  return (
    <main>
      <img src={logo} alt={TITLE} />
      {format(l.cents, "EUR")}
      {rate("EUR")}
      {copy.format(1, "CHF")}
      <Cart />
    </main>
  );
}
