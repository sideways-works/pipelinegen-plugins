// What only a narrow or only a wide screen runs, as a phone and a desktop
// layout of one app do.
export function phoneLayout(): void {
  document.body.dataset.layout = "phone";
}

export function desktopLayout(): void {
  document.body.dataset.layout = "desktop";
}
