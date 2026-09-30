class Fasp::Provider < ApplicationRecord
  # 外部キーは inverse_of の相手(Fasp::Subscription#provider)の列を使う
  has_many :subscriptions, inverse_of: :provider, dependent: :delete_all
end
