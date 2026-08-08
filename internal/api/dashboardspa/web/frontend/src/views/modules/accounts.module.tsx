// Accounts is a core view rather than a firstParty one: dashboardDeps leaves
// EnabledModules unset, so a firstParty descriptor would never mount anywhere.
// Ordered just after Health -- both answer "can the fleet keep working", and an
// operator diagnosing capacity reaches for them together.

import { lazy } from 'react';
import type { FrontendViewDescriptor } from '../types.js';

export const accountsView: FrontendViewDescriptor = {
  id: 'accounts',
  kind: 'core',
  path: '/accounts',
  nav: { label: 'Accounts', order: 65 },
  element: lazy(() => import('../../routes/Accounts').then((m) => ({ default: m.AccountsPage }))),
};
