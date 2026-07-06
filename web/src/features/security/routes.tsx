// features/security — P11 module stub (doc 12 §P11). This file stays the lazy route
// boundary; the real pages replace the Component export in P11.
import { ComingSoon } from "../../app/ComingSoon";

export function Component() {
  return <ComingSoon module="Security" phase="P11" group="sessions.read" />;
}
