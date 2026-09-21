import { defineHandler } from 'nitro/h3'

export default defineHandler(() => ({
  status: 'ok',
  service: 'c8y-diagnostic-agent',
}))
