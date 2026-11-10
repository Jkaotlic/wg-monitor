import { describe,it,expect,vi } from 'vitest'
import { entwareRecoveryAvailable, entwareRecoverySheet } from '../src/entwareRecovery.js'
import { jobTitle,jobView,stepLabel } from '../src/jobSteps.js'
describe('восстановление Entware',()=>{
 it('доступно админу со сломанным/старым агентом',()=>{
  for(const version of ['',null,'v0.66.3-rc2'])expect(entwareRecoveryAvailable({role:'admin',agent_version:version})).toBe(true)
  for(const role of ['owner','operator','viewer'])expect(entwareRecoveryAvailable({role,agent_version:'v9.0.0'})).toBe(false)
 })
 it('просит подтверждение и показывает ход серверного задания',async()=>{
  const start=vi.fn(async()=>({job_id:'fixture'})),done=vi.fn()
  const sheet=entwareRecoverySheet({routerID:7,onDone:done,start})
  expect(sheet.body).toContain('существующие');expect(sheet.body).toContain('hold');expect(start).not.toHaveBeenCalled()
  await sheet.perform();expect(start).toHaveBeenCalledWith(7);sheet.onDone({job_id:'fixture'});expect(done).toHaveBeenCalledWith({job_id:'fixture'})
 })
 it('сохраняет предупреждение даже при успехе',()=>{
  expect(jobTitle('entware_recovery','client-a')).toContain('Entware');expect(stepLabel('recovery_hold')).toContain('BusyBox')
  const view=jobView({kind:'entware_recovery',state:'success',steps:[],hint:'Старая ссылка требует проверки'})
  expect(view.headline).toContain('Entware');expect(view.hint).toContain('Старая ссылка')
 })
})
