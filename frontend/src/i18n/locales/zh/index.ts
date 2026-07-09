import landing from './landing'
import common from './common'
import checkin from './checkin'
import dashboard from './dashboard'
import admin from './admin'
import misc from './misc'

export default {
  ...landing,
  ...common,
  ...checkin,
  ...dashboard,
  admin,
  ...misc,
}
