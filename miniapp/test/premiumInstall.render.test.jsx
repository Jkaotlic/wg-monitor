// @vitest-environment jsdom
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { afterEach, expect, it, vi } from 'vitest'
import { CabinetIssue } from '../src/screens/CabinetIssue.jsx'
import { installPremiumFile, fetchCommandResult, issueVPNConfig } from '../src/api.js'
vi.mock('../src/api.js', () => ({ installPremiumFile:vi.fn(), fetchCommandResult:vi.fn(), issueVPNConfig:vi.fn(), issueAwg3ToRouter:vi.fn(), sendVPNConf:vi.fn() }))
const root=document.createElement('div')
afterEach(() => { render(null,root); vi.resetAllMocks() })
it('устанавливает Premium и ждёт подтверждения агента через общий экран выпуска', async () => {
 installPremiumFile.mockResolvedValue({cmd_id:'premium-command',tunnel_name:'amnezia_nl_synthetic'})
 fetchCommandResult.mockResolvedValue({status:'ok',output:''})
 const onIssued=vi.fn()
 render(<CabinetIssue routerID={7} pending={{provider:'amnezia',keyID:'key1',declaredCountry:'ag',title:'Amnezia Premium',option:{id:'nl',label:'Нидерланды'}}} perms={{sendConf:true}} onIssued={onIssued} />,root)
 expect(root.textContent).toContain('другая страна')
 await act(async () => [...root.querySelectorAll('button')].find(b=>b.textContent==='Выпустить и положить на роутер').click())
 expect(installPremiumFile).toHaveBeenCalledWith(7,'key1','nl','ag')
 expect(fetchCommandResult).toHaveBeenCalledWith(7,'premium-command',10)
 expect(issueVPNConfig).not.toHaveBeenCalled()
 expect(onIssued).toHaveBeenCalledOnce()
 await vi.waitFor(() => expect(root.textContent).toContain('импортирован как'))
 expect(root.textContent).not.toContain('Прислать .conf в личку')
})
