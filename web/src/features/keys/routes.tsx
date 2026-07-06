// features/keys — P9 module stub (doc 12 §P9). This file stays the lazy route
// boundary; the real pages replace the Component export in P9.
import { ComingSoon } from "../../app/ComingSoon";

export function Component() {
  return <ComingSoon module="Keys" phase="P9" group="keys.read" />;
}
