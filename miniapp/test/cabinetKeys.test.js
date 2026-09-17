import { describe, it, expect } from 'vitest'
import {
  CABINET_KINDS,
  VPN_PROVIDER,
  cabinetTabs,
  pickTab,
  cabinetPerms,
  kindText,
  maskText,
  secretRows,
  addSecretFields,
  addSecretReady,
  addSecretRequest,
  cabinetErrorText,
  addDoneText,
  activeDoneText,
  deleteDoneText,
  deleteSecretSheetText,
  revokeSheetText,
  revokeDoneText,
  revokeErrorText,
  issueExplain,
  issueArgs,
  issueFailure,
  sendConfSheetText,
  SEND_CONF_DONE,
  sendConfErrorText,
  CABINET_TEXTS,
} from '../src/cabinetKeys.js'
import { ApiError } from '../src/api.js'

describe('вкладки кабинета', () => {
  it('Amnezia и HideMy всегда; «Свой сервер» -- только когда сервер разрешил', () => {
    expect(cabinetTabs(null)).toEqual([{ id: 'amnezia', title: 'Amnezia' }, { id: 'hidemy', title: 'HideMy' }])
    expect(cabinetTabs({ selfhosted: { available: false } }).map((t) => t.id)).toEqual(['amnezia', 'hidemy'])
    expect(cabinetTabs({ selfhosted: { available: 'yes' } }).map((t) => t.id)).toEqual(['amnezia', 'hidemy'])
    expect(cabinetTabs({ selfhosted: { available: true } })).toEqual([
      { id: 'amnezia', title: 'Amnezia' },
      { id: 'hidemy', title: 'HideMy' },
      { id: 'selfhosted', title: 'Свой сервер' },
    ])
  })

  it('pickTab: пропавшая вкладка -- первая', () => {
    const tabs = cabinetTabs(null)
    expect(pickTab(tabs, 'hidemy')).toBe('hidemy')
    expect(pickTab(tabs, 'selfhosted')).toBe('amnezia')
  })

  it('провайдеры /vpn называются иначе, чем кабинеты', () => {
    expect(CABINET_KINDS).toEqual(['amnezia', 'hidemy'])
    expect(VPN_PROVIDER).toEqual({ amnezia: 'amnezia', hidemy: 'hidemyname', selfhosted: 'selfhosted' })
  })
})

describe('права (спека, решение 1)', () => {
  it('админ и владелец -- всё', () => {
    for (const role of ['admin', 'owner']) {
      expect(cabinetPerms(role)).toEqual({ manage: true, remove: true, revoke: true, sendConf: true })
    }
  })
  it('оператор -- смотреть, добавить, выбрать активный', () => {
    expect(cabinetPerms('operator')).toEqual({ manage: true, remove: false, revoke: false, sendConf: false })
  })
  it('неизвестная роль и пусто -- ничего', () => {
    for (const role of ['', undefined, 'guest']) {
      expect(cabinetPerms(role)).toEqual({ manage: false, remove: false, revoke: false, sendConf: false })
    }
  })
})

describe('ключи и коды', () => {
  it('маска: последние знаки с многоточием, чужие звёздочки срезаются', () => {
    expect(maskText('a1b2')).toBe('…a1b2')
    expect(maskText('…a1b2')).toBe('…a1b2')
    expect(maskText('****a1b2')).toBe('…a1b2')
    expect(maskText('•• a1b2')).toBe('…a1b2')
    // Сверка с частью 1: сервер шлёт «••••» + 4 знака, у короткого секрета -- одни точки.
    expect(maskText('••••a1b2')).toBe('…a1b2')
    expect(maskText('••••')).toBe('')
    expect(maskText('')).toBe('')
    expect(maskText(null)).toBe('')
  })

  it('строки: активный первым, подпись или «без подписи», маска и «активный»', () => {
    const cabinets = {
      amnezia: {
        keys: [
          { id: 'k2', label: '', mask: 'c3d4', active: false },
          { id: 'k1', label: 'основной', mask: 'a1b2', active: true },
        ],
      },
      hidemy: { codes: [{ id: 7, label: 'дача', mask: '6789', active: true }] },
    }
    expect(secretRows(cabinets, 'amnezia')).toEqual([
      { id: 'k1', title: 'основной', sub: 'ключ …a1b2 · активный', active: true },
      { id: 'k2', title: 'Ключ без подписи', sub: 'ключ …c3d4', active: false },
    ])
    expect(secretRows(cabinets, 'hidemy')).toEqual([{ id: '7', title: 'дача', sub: 'код …6789 · активный', active: true }])
  })

  it('пусто и мусор -- пустой список', () => {
    expect(secretRows(null, 'amnezia')).toEqual([])
    expect(secretRows({ amnezia: { keys: 'x' } }, 'amnezia')).toEqual([])
    expect(secretRows({ amnezia: { keys: [] } }, 'selfhosted')).toEqual([])
  })

  it('тексты вида кабинета', () => {
    expect(kindText('amnezia')).toMatchObject({ noun: 'ключ', listKey: 'keys', addButton: 'Добавить ключ', sectionTitle: 'Ключи кабинета' })
    expect(kindText('hidemy')).toMatchObject({ noun: 'код', listKey: 'codes', addButton: 'Добавить код', sectionTitle: 'Коды доступа' })
    expect(kindText('amnezia').empty).toBe('Ключ кабинета Amnezia Premium ещё не добавлен. Добавьте его — и здесь появятся страны, которые можно выпустить.')
    expect(kindText('hidemy').empty).toBe('Код доступа HideMy.name ещё не добавлен. Добавьте его — и здесь появятся серверы, которые можно выпустить.')
  })

  it('поля листа: секрет -- пароль, подпись переживает отказ, секрет -- нет', () => {
    const fields = addSecretFields('amnezia')
    expect(fields.map((f) => f.name)).toEqual(['secret', 'label'])
    expect(fields[0]).toMatchObject({ type: 'password', label: 'Ключ vpn://', placeholder: 'vpn://…' })
    expect(fields[0].keep).toBeUndefined()
    expect(fields[1]).toMatchObject({ keep: true, label: 'Подпись (необязательно)' })
    expect(addSecretFields('hidemy')[0]).toMatchObject({ type: 'password', label: 'Код доступа', placeholder: '' })
  })

  it('кнопка горит: ключ начинается с vpn:// и не пустой после; код -- не пустой', () => {
    const amz = addSecretReady('amnezia')
    expect(amz({ secret: 'vpn://abc' })).toBe(true)
    expect(amz({ secret: '  vpn://abc  ' })).toBe(true)
    expect(amz({ secret: 'vpn://' })).toBe(false)
    expect(amz({ secret: 'abc' })).toBe(false)
    expect(amz({})).toBe(false)
    const hm = addSecretReady('hidemy')
    expect(hm({ secret: ' 123456 ' })).toBe(true)
    expect(hm({ secret: '   ' })).toBe(false)
  })

  it('тело: обрезанные края', () => {
    expect(addSecretRequest({ secret: ' vpn://abc ', label: ' основной ' })).toEqual({ secret: 'vpn://abc', label: 'основной' })
    expect(addSecretRequest({})).toEqual({ secret: '', label: '' })
  })

  it('ошибки: фраза сервера, затем своя по коду, затем общая', () => {
    expect(cabinetErrorText('amnezia', new ApiError(422, 'cabinet_rejected', 'x', 'Подписка истекла'))).toBe('Подписка истекла')
    expect(cabinetErrorText('amnezia', new ApiError(400, 'invalid_key', 'x'))).toBe('Это не ключ Amnezia Premium: ключ начинается с vpn:// и копируется из кабинета целиком.')
    expect(cabinetErrorText('amnezia', new ApiError(422, 'cabinet_rejected', 'x'))).toBe('Кабинет Amnezia Premium не принял ключ. Проверьте, что он скопирован целиком и подписка активна.')
    expect(cabinetErrorText('hidemy', new ApiError(422, 'cabinet_rejected', 'x'))).toBe('HideMy.name не принял код. Проверьте, что он скопирован целиком и подписка не закончилась.')
    expect(cabinetErrorText('hidemy', new ApiError(400, 'invalid_code', 'x'))).toBe('Это не похоже на код доступа HideMy.name.')
    expect(cabinetErrorText('hidemy', new ApiError(404, 'not_found', 'x'))).toBe('Этого уже нет — откройте экран заново.')
    expect(cabinetErrorText('amnezia', new Error('net'))).toBe('Не получилось. Попробуйте ещё раз.')
  })

  it('итоги и лист удаления', () => {
    expect(addDoneText('amnezia')).toBe('Ключ сохранён.')
    expect(addDoneText('hidemy')).toBe('Код сохранён.')
    expect(deleteDoneText('hidemy')).toBe('Код удалён.')
    expect(activeDoneText('amnezia', { title: 'основной' })).toBe('Активный ключ — «основной».')
    expect(deleteSecretSheetText('amnezia', { title: 'основной', active: true })).toEqual({
      title: 'Удалить ключ «основной»?',
      body: 'Это активный ключ: пока не выберете другой, выпускать VPN-туннели из кабинета не получится. Уже выпущенные VPN-туннели на роутере продолжат работать.',
    })
    expect(deleteSecretSheetText('hidemy', { title: 'дача', active: false })).toEqual({
      title: 'Удалить код «дача»?',
      body: 'Код удалится с сервера. Уже выпущенные VPN-туннели на роутере продолжат работать.',
    })
  })
})

describe('отзыв страны', () => {
  it('лист, итог, ошибки', () => {
    expect(revokeSheetText({ id: 'nl', label: 'Нидерланды' }, 'dacha-1')).toEqual({
      title: 'Отозвать «Нидерланды»?',
      body: 'Выпущенный конфиг этой страны перестанет работать везде, где он стоит, и в подписке освободится место. VPN-туннель на роутере «dacha-1» останется, но трафик через него не пойдёт.',
    })
    expect(revokeDoneText({ label: 'Нидерланды' })).toBe('Конфиг «Нидерланды» отозван, место в подписке свободно.')
    expect(revokeErrorText(new ApiError(400, 'confirm_mismatch', 'x'))).toBe('Имя роутера набрано не так.')
    expect(revokeErrorText(new ApiError(502, 'cabinet_failed', 'x', 'Кабинет не ответил'))).toBe('Кабинет не ответил')
    expect(revokeErrorText(new Error('net'))).toBe('Не получилось. Попробуйте ещё раз.')
  })
})

describe('выпуск', () => {
  const amz = { provider: 'amnezia', title: 'Amnezia Premium', option: { id: 'nl', label: 'Нидерланды', note: '' } }
  const again = { ...amz, option: { id: 'de', label: 'Германия', note: 'уже выпущен', issued: true } }
  const own = { provider: 'selfhosted', title: 'Свой сервер', option: { id: 'ams', label: 'Амстердам', note: '' }, instanceID: 'ams' }

  it('объяснение: кабинет, перевыпуск, свой сервер', () => {
    expect(issueExplain(amz)).toBe('Конфиг скачает сервер и сразу отдаст его роутеру — через приложение он не проходит. На роутере появится новый VPN-туннель; прежние остаются на месте.')
    expect(issueExplain(again)).toBe('Конфиг скачает сервер и сразу отдаст его роутеру — через приложение он не проходит. На роутере появится новый VPN-туннель; прежние остаются на месте. Эта страна уже выпускалась: конфиг будет скачан заново.')
    expect(issueExplain(own)).toBe('Сервер создаст на «Амстердам» нового клиента и сразу отдаст конфиг роутеру — через приложение он не проходит. На роутере появится новый VPN-туннель; прежние остаются на месте.')
  })

  it('аргументы: у своего сервера option и instance -- id сервера', () => {
    expect(issueArgs(amz)).toEqual({ provider: 'amnezia', option: 'nl', instanceID: '' })
    expect(issueArgs({ ...amz, provider: 'hidemyname', option: { id: 'srv-1', label: 'x', note: '' } })).toEqual({ provider: 'hidemyname', option: 'srv-1', instanceID: '' })
    expect(issueArgs(own)).toEqual({ provider: 'selfhosted', option: 'ams', instanceID: 'ams' })
  })

  it('slot_busy: тот, кто может отозвать, получает предложение; остальные -- кто может', () => {
    const busy = new ApiError(409, 'slot_busy', 'x', 'Слоты заняты: 3/3')
    expect(issueFailure(busy, { revoke: true })).toEqual({
      text: 'Свободных мест в подписке нет. Отзовите одну из выпущенных стран — и выпуск пройдёт.',
      offerRevoke: true,
    })
    // Отзыв есть только у Amnezia: у других провайдеров предложения нет.
    expect(issueFailure(busy, { revoke: true }, 'hidemyname')).toEqual({ text: 'Свободных мест в подписке нет.', offerRevoke: false })
    expect(issueFailure(busy, { revoke: true }, 'amnezia').offerRevoke).toBe(true)
    expect(issueFailure(busy, { revoke: false })).toEqual({
      text: 'Свободных мест в подписке нет. Отозвать выпущенную страну может владелец роутера или администратор.',
      offerRevoke: false,
    })
  })

  it('прочие отказы: фраза сервера или общая', () => {
    expect(issueFailure(new ApiError(502, 'cabinet_failed', 'x', 'Кабинет не ответил — повторите позже'), { revoke: true })).toEqual({ text: 'Кабинет не ответил — повторите позже', offerRevoke: false })
    expect(issueFailure(new Error('/routers/7/vpn/issue failed: 502'), {})).toEqual({ text: 'Не получилось выпустить конфиг. Попробуйте ещё раз.', offerRevoke: false })
    // Новые коды (свой сервер) несут готовый русский текст -- без приставки «Кабинет отказал».
    expect(issueFailure(new ApiError(409, 'instance_disabled', 'x', 'Сервер выключен.'), {})).toEqual({ text: 'Сервер выключен.', offerRevoke: false })
    // Старые коды выпуска отвечают по-английски -- человеку своя фраза.
    expect(issueFailure(new ApiError(400, 'missing_option', 'x', 'option_id is required'), {})).toEqual({ text: 'Не получилось выпустить конфиг. Попробуйте ещё раз.', offerRevoke: false })
    // Слова сервера -- только у кодов, которые точно говорят по-русски.
    for (const code of ['not_found', 'internal', 'request_too_large', 'bad_json', 'unknown_provider', 'whatever']) {
      expect(issueFailure(new ApiError(400, code, 'x', 'router not found'), {}).text, code).toBe('Не получилось выпустить конфиг. Попробуйте ещё раз.')
    }
    for (const code of ['selfhosted_failed', 'selfhosted_not_configured', 'instance_not_found', 'instance_not_ready', 'missing_instance', 'dm_unreachable', 'invalid_field']) {
      expect(issueFailure(new ApiError(409, code, 'x', 'Русские слова'), {}).text, code).toBe('Русские слова')
    }
  })
})

describe('.conf в личку', () => {
  it('лист: приватный ключ сказан; у своего сервера -- ещё и новый клиент', () => {
    const amz = { provider: 'amnezia', title: 'Amnezia Premium', option: { id: 'nl', label: 'Нидерланды', note: '' } }
    expect(sendConfSheetText(amz)).toEqual({
      title: 'Прислать .conf в личку?',
      body: 'Бот пришлёт файл конфига «Нидерланды» вам в личные сообщения.',
      note: 'В файле приватный ключ: у кого файл, у того и доступ к VPN. Не пересылайте его и не храните в общих чатах.',
    })
    const own = { provider: 'selfhosted', title: 'Свой сервер', option: { id: 'ams', label: 'Амстердам', note: '' }, instanceID: 'ams' }
    expect(sendConfSheetText(own).body).toBe('Бот пришлёт файл конфига «Амстердам» вам в личные сообщения. На сервере для этого будет создан ещё один клиент.')
  })

  it('итог и ошибки', () => {
    expect(SEND_CONF_DONE).toBe('Файл отправлен вам в личку.')
    expect(sendConfErrorText(new ApiError(409, 'dm_unreachable', 'x', 'Бот не может написать вам — нажмите /start'))).toBe('Бот не может написать вам — нажмите /start')
    expect(sendConfErrorText(new ApiError(409, 'dm_unreachable', 'x'))).toBe('Бот не может написать вам — откройте бота, нажмите /start и повторите.')
    expect(sendConfErrorText(new Error('net'))).toBe('Не получилось. Попробуйте ещё раз.')
  })
})

describe('тексты не отправляют в бота', () => {
  it('нигде нет «отправьте боту» и «в боте»', () => {
    const all = [
      ...Object.values(CABINET_TEXTS),
      ...['amnezia', 'hidemy'].flatMap((k) => Object.values(kindText(k)).filter((v) => typeof v === 'string')),
    ].join(' ')
    expect(all).not.toMatch(/боту|в боте/i)
  })
})
