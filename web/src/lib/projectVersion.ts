/** Whether a deployed project version ships an IncomeDistributor: rwa-v3 and rwa-v4 */
export function hasIncomeDistributor(version: string | undefined): boolean {
  return version === "rwa-v3" || version === "rwa-v4";
}
