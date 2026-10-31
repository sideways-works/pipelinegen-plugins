export const PRICES: Record<string, number> = { tea: 3 };

const items: string[] = [];

export function addToCart(name: string, price: number): number {
  items.push(name);
  return items.length * price;
}

export function neverCalled(): number {
  return items.length;
}
